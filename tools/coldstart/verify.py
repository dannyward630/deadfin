#!/usr/bin/env python3
"""tools/coldstart/verify.py — standard-library cold-start API client.

Drives the Deadfin first-run wizard, authenticates, creates the four fixture
libraries with the committed local-only provider policy, triggers one scan,
and asserts that the server serves local content and fixture-backed artwork.

Uses only the Python standard library. The administrator password and the
access token never appear in logs or the job summary.

Each assertion has its own error message and route name. The client stops after
the first failed assertion and never retries a failed wizard, authentication,
mutation, detail, or image request.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


ADMIN_NAME = "ColdstartAdmin"
ADMIN_PASSWORD = "cold-start-change-me"

CLIENT_HEADER = (
    'MediaBrowser Client="Coldstart", Device="Coldstart Verify", '
    'DeviceId="coldstart-verify-v1", Version="1.0.0"'
)

# The four fixture libraries: exact name, collection type, and fixture path.
LIBRARIES = [
    ("Golden Movies", "movies", "/media/movies"),
    ("Golden TV", "tvshows", "/media/tv"),
    ("Golden Music", "music", "/media/music"),
    ("Golden Photos", "homevideos", "/media/photos"),
]

# The exact media path of the fixture-backed Standalone Movie (2023).
STANDALONE_MOVIE_PATH = "/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv"
# The exact media path of the fixture-backed audio track that carries the
# pathological person shapes (NFO Actor without role, probe performer with
# role, NFO Director for the same person).
PATHOLOGICAL_AUDIO_PATH = "/media/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3"
# The exact media path of the fixture-backed movie that carries the
# pathological value shapes (duplicate studios, genres, tags).
PATHOLOGICAL_MOVIE_PATH = STANDALONE_MOVIE_PATH
# The person name shared by the NFO Actor, probe performer, and NFO Director.
PATHOLOGICAL_PERSON_NAME = "Synthetic Performer"
# The probe performer role that fills the empty NFO Actor role.
PATHOLOGICAL_PERSON_ROLE = "Lead Vocalist"
# The expected studio, genre, and tag lists after the value normalizer
# collapses fold-equal duplicates and keeps first-occurrence order and casing.
PATHOLOGICAL_STUDIOS = ["Duplicate Studio", "Case Studio"]
PATHOLOGICAL_GENRES = ["Synth Genre"]
PATHOLOGICAL_TAGS = ["Duplicate Tag"]

# Content item types. A content item is a media item, not a folder or other
# organizational container. fetch_item_detail requires the Type to be one
# of these values.
CONTENT_ITEM_TYPES = frozenset({
    "Movie",
    "Series",
    "Season",
    "Episode",
    "MusicAlbum",
    "MusicArtist",
    "Audio",
    "Photo",
    "BoxSet",
})

READINESS_TIMEOUT = 300
IDLE_POLL_TIMEOUT = 600
IDLE_POLL_INTERVAL = 5

# The scheduled task key that POST /Library/Refresh routes through. The
# deadfin runtime defines this key at internal/tasks/builtins.go. The scan
# records a LastExecutionResult on this task that a client reads over
# GET /ScheduledTasks. See docs/divergences.md D-31.
REFRESH_TASK_KEY = "RefreshLibrary"

# The success and failure status strings from the pinned wire model. The
# deadfin wire model defines these at internal/tasks/scheduler.go. The
# success status is "Completed". The failure status is "Failed".
TASK_SUCCESS_STATUS = "Completed"
TASK_FAILED_STATUS = "Failed"

# The maximum time to wait for a new task result after POST /Library/Refresh.
# The idle poll waits for RefreshStatus to reach Idle. The task result can
# land after the libraries report Idle, so the task timeout is separate.
TASK_RESULT_TIMEOUT = 120
TASK_POLL_INTERVAL = 5


def fail(message):
    print(f"cold-start verify: {message}", file=sys.stderr)
    raise SystemExit(1)


class Client:
    """A thin urllib wrapper that never logs the token or password."""

    def __init__(self, base_url):
        self.base_url = base_url.rstrip("/")

    def _headers(self, accept="application/json", content_type=None, auth=True):
        headers = {"Accept": accept}
        # The MediaBrowser client metadata is always present in the
        # Authorization header. The server rejects AuthenticateByName
        # without it. When auth=True and a token is set, the token is
        # appended after the client metadata.
        if auth and self.token:
            headers["Authorization"] = f'{CLIENT_HEADER}, Token="{self.token}"'
        else:
            headers["Authorization"] = CLIENT_HEADER
        if content_type:
            headers["Content-Type"] = content_type
        return headers

    def request(self, method, path, body=None, expected=None, auth=True,
                accept="application/json"):
        """Send one request. Return (status, raw_bytes, content_type)."""
        url = self.base_url + path
        data = None
        content_type = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            content_type = "application/json"
        headers = self._headers(accept=accept, content_type=content_type, auth=auth)
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        resp_content_type = None
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                status = resp.getcode()
                resp_content_type = resp.headers.get("Content-Type")
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            status = exc.code
            resp_content_type = exc.headers.get("Content-Type")
            raw = exc.read()
        except urllib.error.URLError as exc:
            raise ConnectionError(f"{method} {path} could not reach {self.base_url}: {exc}")
        if expected is not None and status != expected:
            fail(f"{method} {path} expected HTTP {expected}, got {status}")
        return status, raw, resp_content_type

    def json_request(self, method, path, body=None, expected=None, auth=True):
        status, raw, _ = self.request(method, path, body=body, expected=expected,
                                      auth=auth, accept="application/json")
        if not raw:
            return status, None
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            fail(f"{method} {path} returned a non-JSON body")
        return status, None  # unreachable

    def get_json(self, path, expected=200, auth=True):
        return self.json_request("GET", path, expected=expected, auth=auth)[1]

    def post_json(self, path, body=None, expected=204, auth=True):
        return self.json_request("POST", path, body=body, expected=expected,
                                 auth=auth)

    def get_bytes(self, path, expected=200, auth=True):
        status, raw, _ = self.request("GET", path, expected=expected, auth=auth,
                                      accept="*/*")
        return raw

    def get_bytes_with_type(self, path, expected=200, auth=True):
        """Fetch raw bytes and the response Content-Type header in one request."""
        status, raw, content_type = self.request("GET", path, expected=expected,
                                                  auth=auth, accept="*/*")
        return raw, content_type


def wait_for_process(client):
    """Poll only the public information route for process readiness."""
    deadline = time.monotonic() + READINESS_TIMEOUT
    last_error = None
    while time.monotonic() < deadline:
        try:
            status, raw, _ = client.request("GET", "/System/Info/Public", auth=False)
            if status == 200 and raw:
                info = json.loads(raw)
                version = info.get("Version")
                if version:
                    return info
        except (ConnectionError, urllib.error.URLError, json.JSONDecodeError) as exc:
            last_error = exc
        time.sleep(2)
    fail(f"server did not become ready on /System/Info/Public within {READINESS_TIMEOUT}s"
         f"{f'; last error: {last_error}' if last_error else ''}")


def run_wizard(client):
    """Send each wizard mutation exactly once."""
    # /Startup/Configuration
    client.post_json(
        "/Startup/Configuration",
        body={
            "ServerName": "Coldstart Gate",
            "UICulture": "en-US",
            "MetadataCountryCode": "US",
            "PreferredMetadataLanguage": "en",
        },
        expected=204,
        auth=False,
    )
    # /Startup/User — create the administrator.
    client.post_json(
        "/Startup/User",
        body={"Name": ADMIN_NAME, "Password": ADMIN_PASSWORD},
        expected=204,
        auth=False,
    )
    # /Startup/Complete — finalize the wizard. No body, as in the proven seed.
    client.post_json("/Startup/Complete", body=None, expected=204, auth=False)


def authenticate(client):
    """Authenticate once through /Users/AuthenticateByName. Require a nonempty access token."""
    status, raw, _ = client.request(
        "POST", "/Users/AuthenticateByName",
        body={"Username": ADMIN_NAME, "Pw": ADMIN_PASSWORD},
        expected=200, auth=False,
    )
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        fail("/Users/AuthenticateByName returned a non-JSON body")
    token = data.get("AccessToken")
    user = data.get("User")
    user_id = user.get("Id") if isinstance(user, dict) else None
    if not isinstance(token, str) or not token:
        fail("/Users/AuthenticateByName returned an empty AccessToken")
    if not isinstance(user_id, str) or not user_id:
        fail("/Users/AuthenticateByName returned an empty User.Id")
    client.token = token
    client.user_id = user_id


def create_library(client, name, kind, path, policy):
    """Create one library with the committed local-only provider policy."""
    query = urllib.parse.urlencode({
        "name": name,
        "collectionType": kind,
        "paths": path,
        "refreshLibrary": "false",
    })
    client.post_json(
        f"/Library/VirtualFolders?{query}",
        body={"LibraryOptions": policy},
        expected=204,
        auth=True,
    )


def read_libraries(client):
    """Read the four libraries. Require each exact name, type, path, and stable item ID."""
    libraries = client.get_json("/Library/VirtualFolders", expected=200, auth=True)
    if not isinstance(libraries, list):
        fail("/Library/VirtualFolders returned a non-array body")
    by_name = {}
    for library in libraries:
        if not isinstance(library, dict):
            fail("/Library/VirtualFolders returned a non-object library entry")
        lib_name = library.get("Name")
        if isinstance(lib_name, str):
            by_name.setdefault(lib_name, []).append(library)
    result = {}
    for name, kind, path in LIBRARIES:
        matches = by_name.get(name, [])
        if not matches:
            fail(f"/Library/VirtualFolders has no library named '{name}'")
        if len(matches) != 1:
            fail(f"/Library/VirtualFolders has {len(matches)} libraries named '{name}'")
        library = matches[0]
        locations = library.get("Locations") or library.get("Paths") or []
        if locations != [path]:
            fail(f"library '{name}' has paths {locations!r}, expected {[path]!r}")
        if library.get("CollectionType") != kind:
            fail(f"library '{name}' has collection type {library.get('CollectionType')!r}, expected {kind!r}")
        item_id = library.get("ItemId") or library.get("Id")
        if not isinstance(item_id, str) or not item_id:
            fail(f"library '{name}' lacks a stable item ID")
        result[name] = {"id": item_id, "library": library}
    return result


def apply_provider_policy(client, libraries, policy):
    """Apply the committed local-only provider policy to each library."""
    for name, _kind, _path in LIBRARIES:
        item_id = libraries[name]["id"]
        client.post_json(
            "/Library/VirtualFolders/LibraryOptions",
            body={"Id": item_id, "LibraryOptions": policy},
            expected=204,
            auth=True,
        )


def trigger_refresh(client):
    """Send one POST /Library/Refresh request and log it as evidence."""
    print("cold-start verify: POST /Library/Refresh")
    client.post_json(
        "/Library/Refresh?metadataRefreshMode=Default&imageRefreshMode=Default",
        body=None,
        expected=204,
        auth=True,
    )



def fetch_scheduled_tasks(client):
    """Read GET /ScheduledTasks and return the parsed task list."""
    data = client.get_json("/ScheduledTasks", expected=200, auth=True)
    if not isinstance(data, list):
        fail("/ScheduledTasks returned a non-array body")
    return data


def select_refresh_task(tasks):
    """Select exactly one task whose Key equals RefreshLibrary.

    Return the task dict. Fail on zero matches, two or more matches, or a
    task entry that is not a dict.
    """
    matches = []
    for task in tasks:
        if not isinstance(task, dict):
            fail("/ScheduledTasks returned a non-object task entry")
        if task.get("Key") == REFRESH_TASK_KEY:
            matches.append(task)
    if len(matches) == 0:
        fail(f"/ScheduledTasks has no task with Key {REFRESH_TASK_KEY!r}")
    if len(matches) != 1:
        fail(f"/ScheduledTasks has {len(matches)} tasks with Key {REFRESH_TASK_KEY!r}, expected 1")
    return matches[0]


def _require_task_timestamp(result, field):
    """Return the timestamp string or fail on a missing, empty, or non-string value.

    A valid LastExecutionResult records StartTimeUtc and EndTimeUtc as
    non-empty strings. A result that omits them or carries a non-string
    value is malformed and must not pass the freshness comparison.
    """
    value = result.get(field)
    if not isinstance(value, str) or not value:
        fail(f"RefreshLibrary LastExecutionResult {field} is missing, empty, or not a string: {value!r}")
    return value


def capture_prior_task_result(client):
    """Read the current RefreshLibrary LastExecutionResult before the refresh.

    Return the result dict, or None when the task has no prior result. The
    scan phase compares the post-refresh result against this snapshot to
    reject a stale result that predates POST /Library/Refresh.

    When a prior result exists, require StartTimeUtc and EndTimeUtc to be
    non-empty strings so the freshness comparison has valid data.
    """
    task = select_refresh_task(fetch_scheduled_tasks(client))
    result = task.get("LastExecutionResult")
    if result is None:
        return None
    if not isinstance(result, dict):
        fail(f"RefreshLibrary LastExecutionResult is not an object: {result!r}")
    _require_task_timestamp(result, "StartTimeUtc")
    _require_task_timestamp(result, "EndTimeUtc")
    return result


def wait_for_task_result(client, prior_result):
    """Wait for a RefreshLibrary result that differs from the prior snapshot.

    Poll GET /ScheduledTasks until the RefreshLibrary task has a
    LastExecutionResult that differs from prior_result, and the task State
    is Idle. Use StartTimeUtc and EndTimeUtc to reject a stale result.

    Require StartTimeUtc and EndTimeUtc to be non-empty strings before the
    stale comparison. A result that omits them is malformed and must not
    pass freshness.

    Return the new result dict. Fail on a timeout, a missing result, or an
    unchanged result.
    """
    deadline = time.monotonic() + TASK_RESULT_TIMEOUT
    prior_start = prior_result.get("StartTimeUtc") if prior_result else None
    prior_end = prior_result.get("EndTimeUtc") if prior_result else None
    while time.monotonic() < deadline:
        task = select_refresh_task(fetch_scheduled_tasks(client))
        result = task.get("LastExecutionResult")
        if isinstance(result, dict):
            start = _require_task_timestamp(result, "StartTimeUtc")
            end = _require_task_timestamp(result, "EndTimeUtc")
            state = task.get("State")
            # Reject a stale result that has the same timestamps as the
            # prior snapshot. A new run records new StartTimeUtc and
            # EndTimeUtc values.
            is_stale = (start == prior_start and end == prior_end)
            if not is_stale and state == "Idle":
                return result
        time.sleep(TASK_POLL_INTERVAL)
    fail(f"RefreshLibrary task result did not change within {TASK_RESULT_TIMEOUT}s")


def assert_task_success(result):
    """Require Status == Completed and an empty ErrorMessage.

    If the observed status is Failed, require a nonempty ErrorMessage
    before this assertion returns the negative dispatch failure. This
    makes the negative control fail on the task-result assertion, not on
    the item count.
    """
    status = result.get("Status")
    error = result.get("ErrorMessage")
    if not isinstance(status, str) or not status:
        fail(f"RefreshLibrary LastExecutionResult has no Status: {result!r}")
    if not isinstance(error, str):
        fail(f"RefreshLibrary LastExecutionResult ErrorMessage is not a string: {error!r}")
    # A Failed result must carry a nonempty error. This is the contract
    # deadfin D-31 added. Assert it before the success-status check so the
    # negative control fails on the task result, not on a missing field.
    if status == TASK_FAILED_STATUS:
        if not error.strip():
            fail(f"RefreshLibrary status is {status!r} but the ErrorMessage is empty")
    # The final success-status assertion. A Completed result with an empty
    # error passes. Any other status fails.
    if status != TASK_SUCCESS_STATUS:
        fail(f"RefreshLibrary success-status assertion: Status is {status!r}, expected {TASK_SUCCESS_STATUS!r}")
    if error:
        fail(f"RefreshLibrary success-status assertion: ErrorMessage is {error!r}, expected empty")


def library_states(client):
    """Read the four RefreshStatus values."""
    libraries = client.get_json("/Library/VirtualFolders", expected=200, auth=True)
    if not isinstance(libraries, list):
        fail("/Library/VirtualFolders returned a non-array body during polling")
    by_name = {}
    for library in libraries:
        if isinstance(library, dict) and isinstance(library.get("Name"), str):
            by_name[library["Name"]] = library
    states = {}
    for name, _kind, _path in LIBRARIES:
        states[name] = (by_name.get(name) or {}).get("RefreshStatus", "missing")
    return states


def list_items(client):
    """List recursive items. Return (TotalRecordCount, items list)."""
    data = client.get_json(
        "/Items?Recursive=true&Fields=Path&EnableTotalRecordCount=true",
        expected=200,
        auth=True,
    )
    if not isinstance(data, dict):
        fail("/Items returned a non-object body")
    total = data.get("TotalRecordCount")
    items = data.get("Items")
    if not isinstance(total, int) or total < 0:
        fail(f"/Items returned an invalid TotalRecordCount: {total!r}")
    if not isinstance(items, list):
        fail("/Items returned a non-array Items field")
    return total, items


def poll_until_stable_idle(client):
    """Poll library state and item inventory until two consecutive observations are stable and idle."""
    deadline = time.monotonic() + IDLE_POLL_TIMEOUT
    prior_states = None
    prior_total = None
    while time.monotonic() < deadline:
        states = library_states(client)
        if any(state == "missing" for state in states.values()):
            fail(f"library disappeared during polling: {states}")
        if any(state in ("Active", "Queued") for state in states.values()):
            # A non-idle observation breaks the consecutive-idle sequence.
            # Reset the prior observation so a later idle does not match a
            # stale idle sample from before the active period.
            prior_states = None
            prior_total = None
            time.sleep(IDLE_POLL_INTERVAL)
            continue
        # All four Idle here. Now check the inventory is stable too.
        total, items = list_items(client)
        if prior_states is None:
            prior_states = states
            prior_total = total
            time.sleep(IDLE_POLL_INTERVAL)
            continue
        if states == prior_states and total == prior_total:
            return states, total, items
        prior_states = states
        prior_total = total
        time.sleep(IDLE_POLL_INTERVAL)
        continue
    fail(f"libraries did not reach a stable idle state within {IDLE_POLL_TIMEOUT}s")


def select_content_item_id(items):
    """Select the first item whose Type is a content type, for the detail fetch.

    The recursive listing includes the physical Folder item for each library
    root. Selecting the first item of any type would assert a content type
    against a Folder, so the selection filters on the same set the assertion
    uses.
    """
    for item in items:
        if not isinstance(item, dict):
            continue
        item_id = item.get("Id")
        if not isinstance(item_id, str) or not item_id:
            continue
        if item.get("Type") in CONTENT_ITEM_TYPES:
            return item_id
    fail(
        "/Items returned no item with a stable Id and a content item type "
        f"{sorted(CONTENT_ITEM_TYPES)}"
    )


def fetch_item_detail(client, item_id):
    """Fetch that item's detail. Require the same item ID and a content item type."""
    data = client.get_json(f"/Items/{item_id}", expected=200, auth=True)
    if not isinstance(data, dict):
        fail(f"/Items/{item_id} returned a non-object body")
    if data.get("Id") != item_id:
        fail(f"/Items/{item_id} returned Id {data.get('Id')!r}, expected {item_id!r}")
    item_type = data.get("Type")
    if not isinstance(item_type, str) or not item_type:
        fail(f"/Items/{item_id} returned an empty Type")
    if item_type not in CONTENT_ITEM_TYPES:
        fail(f"/Items/{item_id} returned Type {item_type!r}, expected a content item type {CONTENT_ITEM_TYPES}")
    return data


def find_item_by_path(items, media_path, label):
    """Select a fixture-backed content item by its exact media path."""
    for item in items:
        if not isinstance(item, dict):
            continue
        if item.get("Path") == media_path:
            item_id = item.get("Id")
            if not isinstance(item_id, str) or not item_id:
                fail(f"{label} item lacks a stable Id")
            return item_id
    fail(f"no item matched the exact media path {media_path}")


def find_standalone_movie(client, items):
    """Select the fixture-backed Standalone Movie (2023) by its exact media path."""
    return find_item_by_path(items, STANDALONE_MOVIE_PATH, "Standalone Movie (2023)")


def fetch_primary_image(client, item_id):
    """Fetch its Primary image. Require HTTP 200, a nonempty body, and an image content type."""
    raw, content_type = client.get_bytes_with_type(
        f"/Items/{item_id}/Images/Primary", expected=200, auth=True,
    )
    if not raw:
        fail(f"/Items/{item_id}/Images/Primary returned an empty body")
    # Require an image content type from the response header.
    if not isinstance(content_type, str) or not content_type.startswith("image/"):
        fail(f"/Items/{item_id}/Images/Primary returned content type {content_type!r}, expected an image type")
    return len(raw), content_type


def verify_pathological_person(client, items):
    """Verify the person shapes on the pathological audio track.

    Require one Actor and one Director for the same person. Require the
    Actor role from the probe metadata. The NFO Actor had no role. The
    probe performer filled it. The Director survived because the collapse
    key includes PersonKind.
    """
    item_id = find_item_by_path(items, PATHOLOGICAL_AUDIO_PATH, "pathological audio track")
    detail = fetch_item_detail(client, item_id)
    people = detail.get("People")
    if not isinstance(people, list):
        fail(f"pathological audio track People is not a list: {people!r}")
    actors = [
        p for p in people
        if isinstance(p, dict) and p.get("Type") == "Actor" and p.get("Name") == PATHOLOGICAL_PERSON_NAME
    ]
    directors = [
        p for p in people
        if isinstance(p, dict) and p.get("Type") == "Director" and p.get("Name") == PATHOLOGICAL_PERSON_NAME
    ]
    if len(actors) != 1:
        fail(f"pathological audio track has {len(actors)} Actors for {PATHOLOGICAL_PERSON_NAME}, expected 1")
    if len(directors) != 1:
        fail(f"pathological audio track has {len(directors)} Directors for {PATHOLOGICAL_PERSON_NAME}, expected 1")
    actor_role = actors[0].get("Role")
    if actor_role != PATHOLOGICAL_PERSON_ROLE:
        fail(f"pathological audio track Actor role is {actor_role!r}, expected {PATHOLOGICAL_PERSON_ROLE!r}")
    print(f"cold-start verify: person={PATHOLOGICAL_PERSON_NAME} actor=1 role={actor_role} director=1")
    return item_id


def verify_pathological_values(client, items):
    """Verify the value shapes on the pathological movie.

    Require the expected studio, genre, and tag lists. Compare their
    order and exact casing. The normalizer collapsed fold-equal
    duplicates and kept the first occurrence.
    """
    item_id = find_item_by_path(items, PATHOLOGICAL_MOVIE_PATH, "pathological movie")
    detail = fetch_item_detail(client, item_id)
    studios = [
        s.get("Name") if isinstance(s, dict) else s
        for s in (detail.get("Studios") or [])
    ]
    genres = detail.get("Genres") or []
    tags = detail.get("Tags") or []
    if studios != PATHOLOGICAL_STUDIOS:
        fail(f"pathological movie studios are {studios!r}, expected {PATHOLOGICAL_STUDIOS!r}")
    if genres != PATHOLOGICAL_GENRES:
        fail(f"pathological movie genres are {genres!r}, expected {PATHOLOGICAL_GENRES!r}")
    if tags != PATHOLOGICAL_TAGS:
        fail(f"pathological movie tags are {tags!r}, expected {PATHOLOGICAL_TAGS!r}")
    print(f"cold-start verify: values=studios={studios} genres={genres} tags={tags}")
    return item_id

def write_summary(digest, commit, item_count, states, task_result):
    """Write the digest, commit, item count, four Idle states, and task result.

    Print the exact task status and error message. Do not print credentials
    or authentication data.
    """
    commit_line = f"- Commit SHA: `{commit}`" if commit else "- Commit SHA: `<none>`"
    task_status = task_result.get("Status", "<none>") if task_result else "<none>"
    task_error = task_result.get("ErrorMessage", "") if task_result else ""
    lines = [
        "## Cold-start gate result",
        "",
        f"- Image digest: `{digest}`",
        commit_line,
        f"- Item count: {item_count}",
        f"- Golden Movies: {states['Golden Movies']}",
        f"- Golden TV: {states['Golden TV']}",
        f"- Golden Music: {states['Golden Music']}",
        f"- Golden Photos: {states['Golden Photos']}",
        f"- RefreshLibrary Status: {task_status}",
        f"- RefreshLibrary ErrorMessage: {task_error!r}",
        "- RestartCount: 0",
    ]
    summary = "\n".join(lines) + "\n"
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        try:
            with open(summary_path, "a", encoding="utf-8") as handle:
                handle.write(summary)
        except OSError as exc:
            fail(f"could not write the job summary to {summary_path}: {exc}")
    # Always print the summary so the logs carry it.
    print(summary, end="")


def load_policy(policy_path):
    """Load the committed local-only provider policy."""
    try:
        with open(policy_path, "r", encoding="utf-8") as handle:
            policy = json.load(handle)
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"could not read the committed provider policy at {policy_path}: {exc}")
    # Enforce that the policy permits no live provider traffic.
    if policy.get("EnableInternetProviders") is not False:
        fail("provider policy must set EnableInternetProviders to false")
    type_options = policy.get("TypeOptions")
    if not isinstance(type_options, list):
        fail("provider policy must contain TypeOptions")
    for option in type_options:
        if not isinstance(option, dict):
            fail("provider policy TypeOptions must be objects")
        for key in ("MetadataFetchers", "ImageFetchers", "SimilarItemProviders"):
            if option.get(key) != []:
                fail(f"provider policy must disable {key} for {option.get('Type')}")
    return policy


def run_configuration(base_url, policy_path):
    """Run the configuration phase: readiness, wizard, admin, four libraries.

    Create the administrator and the four fixture libraries with the
    committed local-only provider policy. Return the base URL for the scan
    phase. The scan phase authenticates again.
    """
    policy = load_policy(policy_path)
    client = Client(base_url)

    # Poll only the public information route for process readiness.
    wait_for_process(client)

    # Wizard: each mutation exactly once.
    run_wizard(client)

    # Authenticate once. Require a nonempty access token.
    authenticate(client)

    # Create the four fixture libraries with the committed local-only policy.
    for name, kind, path in LIBRARIES:
        create_library(client, name, kind, path, policy)

    # Apply the committed local-only provider policy.
    libraries = read_libraries(client)
    apply_provider_policy(client, libraries, policy)

    # Read the four libraries again to confirm the policy stuck.
    read_libraries(client)


def run_scan(base_url, policy_path, digest, commit):
    """Run the scan phase: authenticate, capture prior result, refresh, assert.

    Authenticate again, capture the prior RefreshLibrary task result,
    trigger one refresh, wait for stable Idle, keep the existing Idle and
    nonzero-count assertions, write the summary with the task result, then
    require the success status.
    """
    client = Client(base_url)

    # Authenticate again for the scan phase.
    authenticate(client)

    # Capture the current LastExecutionResult before POST /Library/Refresh.
    prior_result = capture_prior_task_result(client)

    # Send one POST /Library/Refresh request.
    trigger_refresh(client)

    # Poll until two consecutive observations are stable and idle.
    states, item_count, items = poll_until_stable_idle(client)

    # Require all four final RefreshStatus values to equal Idle.
    for name, _kind, _path in LIBRARIES:
        if states[name] != "Idle":
            fail(f"library '{name}' final RefreshStatus is {states[name]!r}, expected 'Idle'")

    # List recursive items. Require a nonzero TotalRecordCount and one stable item ID.
    if item_count <= 0:
        fail(f"/Items TotalRecordCount is {item_count}, expected a nonzero count")
    # Wait for the RefreshLibrary task result to change and reach Idle.
    task_result = wait_for_task_result(client, prior_result)

    # If the observed status is Failed, require a nonempty ErrorMessage
    # before writing the summary. This proves the Failed contract that
    # deadfin D-31 added. The summary then carries the observed result.
    if task_result.get("Status") == TASK_FAILED_STATUS:
        error = task_result.get("ErrorMessage")
        if not isinstance(error, str) or not error.strip():
            fail(f"RefreshLibrary status is {TASK_FAILED_STATUS!r} but the ErrorMessage is empty")

    # Write the item count and observed task result to the summary. Do this
    # before the success-status assertion so the evidence bundle carries
    # the observed result even when the gate fails.
    write_summary(digest, commit, item_count, states, task_result)

    # The final success-status assertion. A Completed result with an empty
    # error passes. Any other status fails and returns nonzero. This is
    # the assertion that returns the negative dispatch failure.
    assert_task_success(task_result)

    # Pick one stable content item ID for the detail fetch. The listing also
    # carries the physical Folder item for each library root, so selecting the
    # first item of any type would assert a content type against a Folder.
    detail_item_id = select_content_item_id(items)

    # Fetch that item's detail. Require the same item ID and a content item type.
    fetch_item_detail(client, detail_item_id)

    # Select the fixture-backed Standalone Movie (2023) by its exact media path.
    standalone_id = find_standalone_movie(client, items)

    # Fetch its Primary image. Require HTTP 200, a nonempty body, and an image content type.
    image_size, image_type = fetch_primary_image(client, standalone_id)

    print(f"cold-start verify: item_count={item_count}")
    print(f"cold-start verify: image bytes={image_size} content_type={image_type}")
    print(f"cold-start verify: detail_item_id={detail_item_id}")
    print(f"cold-start verify: standalone_movie_id={standalone_id}")
    # Verify the six pathological metadata shapes after the stable-idle scan.
    pathological_audio_id = verify_pathological_person(client, items)
    pathological_movie_id = verify_pathological_values(client, items)

    print("cold-start verify: all assertions passed")


def main():
    if len(sys.argv) < 5:
        fail("usage: verify.py <base_url> <container_name> <run_label> <policy_path> [configure|scan|both]")
    base_url = sys.argv[1]
    policy_path = sys.argv[4]
    phase = sys.argv[5] if len(sys.argv) > 5 else "both"
    digest = os.environ.get("IMAGE_DIGEST", "<unknown>")
    commit = os.environ.get("COMMIT_SHA", "")

    if phase not in ("configure", "scan", "both"):
        fail(f"unknown phase {phase!r}, expected configure, scan, or both")

    if phase in ("configure", "both"):
        # Configuration phase: create the administrator and the four libraries.
        run_configuration(base_url, policy_path)

    if phase in ("scan", "both"):
        # Scan phase: authenticate again, capture the prior task result,
        # trigger one refresh, and run all assertions.
        run_scan(base_url, policy_path, digest, commit)


if __name__ == "__main__":
    main()
