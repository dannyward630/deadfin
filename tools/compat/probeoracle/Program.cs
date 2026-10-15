using System.Diagnostics;
using System.Reflection;
using System.Runtime.Loader;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Jellyfin.Extensions.Json;
using Jellyfin.Extensions.Json.Converters;
using MediaBrowser.MediaEncoding.Probing;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.Globalization;
using Microsoft.Extensions.Logging.Abstractions;
using MediaBrowser.Model.MediaInfo;

const string ExpectedCommit = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb";
string[] FixtureNames =
[
    "video_metadata.json",
    "video_mp4_metadata.json",
    "video_ts.json",
    "video_webm.json",
    "video_web_like_mkv_with_subtitle.json",
    "video_missing_video_bitrate.json",
    "video_nanosecond_duration_bitrate.json",
    "video_missing_video_bitrate_unknown_audio.json"
];
var FixturePaths = new SortedDictionary<string, string>(StringComparer.Ordinal)
{
    ["video_metadata.json"] = "Test Data/Probing/video_metadata.mkv",
    ["video_missing_video_bitrate.json"] = "Test Data/Probing/video_missing_video_bitrate.mp4",
    ["video_missing_video_bitrate_unknown_audio.json"] = "Test Data/Probing/video_missing_video_bitrate_unknown_audio.mp4",
    ["video_mp4_metadata.json"] = "Test Data/Probing/video_mp4_metadata.mkv",
    ["video_nanosecond_duration_bitrate.json"] = "Test Data/Probing/video_nanosecond_duration_bitrate.mkv",
    ["video_ts.json"] = "Test Data/Probing/video_metadata.mkv",
    ["video_web_like_mkv_with_subtitle.json"] = "Test Data/Probing/video_metadata.mkv",
    ["video_webm.json"] = "Test Data/Probing/video_metadata.webm"
};

var arguments = ParseArguments(args);
var fixtureRoot = RequiredDirectory(arguments, "--fixture-root");
var outputRoot = Path.GetFullPath(RequiredValue(arguments, "--output-root"));
var upstreamRoot = RequiredDirectory(arguments, "--upstream");
var oracleSourceRoot = RequiredDirectory(arguments, "--oracle-source-root");
var manifestPath = RequiredFile(arguments, "--build-manifest");
var command = RequiredValue(arguments, "--command");

if (Directory.Exists(outputRoot))
{
    throw new InvalidOperationException($"output root must not exist before generation: {outputRoot}");
}

ValidatePinnedCleanUpstream(upstreamRoot);
ValidateFixtureInventory(fixtureRoot, FixtureNames);
var buildManifest = ReadBuildManifest(manifestPath);
ValidateFreshBuildLocation(buildManifest);
ValidateRequiredBehaviorAssemblies();
ValidateBuildOutputAssemblies(buildManifest);
var sourceHashes = BuildSourceHashes(fixtureRoot, upstreamRoot, oracleSourceRoot, manifestPath, FixtureNames);

var inputJson = new JsonSerializerOptions(JsonDefaults.Options);
inputJson.Converters.Add(new JsonBoolStringConverter());
var outputJson = new JsonSerializerOptions(JsonDefaults.Options)
{
    WriteIndented = true
};
var normalizer = new ProbeResultNormalizer(NullLogger.Instance, DispatchProxy.Create<ILocalizationManager, EchoLocalizationManager>());
var generated = new SortedDictionary<string, byte[]>(StringComparer.Ordinal);
foreach (var fixtureName in FixtureNames)
{
    var fixturePath = Path.Combine(fixtureRoot, fixtureName);
    var result = JsonSerializer.Deserialize<InternalMediaInfoResult>(File.ReadAllBytes(fixturePath), inputJson)
        ?? throw new InvalidOperationException($"fixture deserialized to null: {fixtureName}");
    var mediaInfo = normalizer.GetMediaInfo(result, VideoType.VideoFile, false, FixturePaths[fixtureName], MediaProtocol.File);
    generated.Add(fixtureName, Utf8(Serialize(mediaInfo, outputJson)));
}

var loadedAssemblyHashes = ValidateLoadedBehaviorAssemblies(buildManifest);
var outputHashes = generated.ToDictionary(pair => pair.Key, pair => Sha256Hex(pair.Value), StringComparer.Ordinal);
var provenance = new Provenance(
    ExpectedCommit,
    ReadGit(upstreamRoot, "rev-parse", "HEAD"),
    sourceHashes,
    buildManifest.Assemblies,
    loadedAssemblyHashes,
    command,
    $"{System.Runtime.InteropServices.RuntimeInformation.FrameworkDescription}; Environment.Version={Environment.Version}",
    "Pinned Jellyfin.Extensions.Json.JsonDefaults.Options with WriteIndented=true, UTF-8 without BOM, LF line endings",
    FixturePaths,
    outputHashes);
var provenanceBytes = Utf8(Serialize(provenance, outputJson));

Directory.CreateDirectory(outputRoot);
foreach (var (fixtureName, bytes) in generated)
{
    File.WriteAllBytes(Path.Combine(outputRoot, fixtureName), bytes);
}

File.WriteAllBytes(Path.Combine(outputRoot, "provenance.json"), provenanceBytes);

static Dictionary<string, string> ParseArguments(string[] args)
{
    if (args.Length != 12)
    {
        throw new ArgumentException("usage: ProbeOracle --fixture-root DIR --output-root DIR --upstream DIR --oracle-source-root DIR --build-manifest FILE --command TEXT");
    }

    var values = new Dictionary<string, string>(StringComparer.Ordinal);
    for (var index = 0; index < args.Length; index += 2)
    {
        if (!args[index].StartsWith("--", StringComparison.Ordinal) || !values.TryAdd(args[index], args[index + 1]))
        {
            throw new ArgumentException("arguments must be unique --name value pairs");
        }
    }

    return values;
}

static string RequiredValue(IReadOnlyDictionary<string, string> arguments, string name)
    => arguments.TryGetValue(name, out var value) && !string.IsNullOrWhiteSpace(value)
        ? value
        : throw new ArgumentException($"missing required argument {name}");

static string RequiredDirectory(IReadOnlyDictionary<string, string> arguments, string name)
{
    var value = Path.GetFullPath(RequiredValue(arguments, name));
    return Directory.Exists(value) ? value : throw new DirectoryNotFoundException($"required directory is missing: {value}");
}

static string RequiredFile(IReadOnlyDictionary<string, string> arguments, string name)
{
    var value = Path.GetFullPath(RequiredValue(arguments, name));
    return File.Exists(value) ? value : throw new FileNotFoundException("required file is missing", value);
}

static void ValidatePinnedCleanUpstream(string upstreamRoot)
{
    if (!string.Equals(ReadGit(upstreamRoot, "rev-parse", "HEAD"), ExpectedCommit, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"upstream HEAD is not pinned commit {ExpectedCommit}");
    }

    if (!string.IsNullOrEmpty(ReadGit(upstreamRoot, "status", "--porcelain", "--untracked-files=all")))
    {
        throw new InvalidOperationException("upstream worktree is dirty");
    }
}

static string ReadGit(string root, params string[] arguments)
{
    var startInfo = new ProcessStartInfo("git")
    {
        WorkingDirectory = root,
        RedirectStandardOutput = true,
        RedirectStandardError = true,
        UseShellExecute = false
    };
    foreach (var argument in arguments)
    {
        startInfo.ArgumentList.Add(argument);
    }

    using var process = Process.Start(startInfo) ?? throw new InvalidOperationException("could not start git");
    var output = process.StandardOutput.ReadToEnd();
    var error = process.StandardError.ReadToEnd();
    process.WaitForExit();
    if (process.ExitCode != 0)
    {
        throw new InvalidOperationException($"git {string.Join(' ', arguments)} failed: {error}");
    }

    return output.Trim();
}

static void ValidateFixtureInventory(string fixtureRoot, IReadOnlyCollection<string> fixtureNames)
{
    var inventoryPath = Path.Combine(fixtureRoot, "inventory.json");
    using var inventory = JsonDocument.Parse(File.ReadAllBytes(inventoryPath));
    var sourceCommit = inventory.RootElement.GetProperty("source_commit").GetString();
    if (!string.Equals(sourceCommit, ExpectedCommit, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"fixture inventory source_commit is not {ExpectedCommit}");
    }

    var actual = inventory.RootElement.GetProperty("fixtures").EnumerateArray().Select(value => value.GetString()).ToArray();
    if (!actual.SequenceEqual(fixtureNames, StringComparer.Ordinal))
    {
        throw new InvalidOperationException("fixture inventory does not contain the exact pinned probe fixture set");
    }

    foreach (var fixtureName in fixtureNames)
    {
        if (!File.Exists(Path.Combine(fixtureRoot, fixtureName)))
        {
            throw new FileNotFoundException($"pinned probe fixture is missing: {fixtureName}");
        }
    }
}

static FreshBuildManifest ReadBuildManifest(string manifestPath)
{
    var manifest = JsonSerializer.Deserialize<FreshBuildManifest>(File.ReadAllBytes(manifestPath))
        ?? throw new InvalidOperationException("fresh build manifest is empty");
    if (string.IsNullOrWhiteSpace(manifest.BuildRoot) || manifest.Assemblies is null || manifest.Assemblies.Count == 0)
    {
        throw new InvalidOperationException("fresh build manifest lacks buildRoot or assembly hashes");
    }

    return manifest with { BuildRoot = Path.GetFullPath(manifest.BuildRoot) };
}

static void ValidateFreshBuildLocation(FreshBuildManifest manifest)
{
    var baseDirectory = Path.GetFullPath(AppContext.BaseDirectory);
    var relative = Path.GetRelativePath(manifest.BuildRoot, baseDirectory);
    if (relative == ".." || relative.StartsWith($"..{Path.DirectorySeparatorChar}", StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"executing oracle is outside the fresh build root: {baseDirectory}");
    }

    if (Directory.EnumerateDirectories(manifest.BuildRoot, "bin", SearchOption.AllDirectories).Any()
        || Directory.EnumerateDirectories(manifest.BuildRoot, "obj", SearchOption.AllDirectories).Any())
    {
        throw new InvalidOperationException("fresh build root contains stale bin or obj artifacts");
    }
}

static void ValidateRequiredBehaviorAssemblies()
{
    var required = new[]
    {
        "ProbeOracle",
        "MediaBrowser.MediaEncoding",
        "MediaBrowser.Common",
        "MediaBrowser.Controller",
        "MediaBrowser.Model",
        "Jellyfin.Extensions"
    };
    foreach (var name in required)
    {
        AssemblyLoadContext.Default.LoadFromAssemblyName(new AssemblyName(name));
    }

    var loaded = AppDomain.CurrentDomain.GetAssemblies().Select(assembly => assembly.GetName().Name).ToHashSet(StringComparer.Ordinal);
    foreach (var name in required)
    {
        if (!loaded.Contains(name))
        {
            throw new InvalidOperationException($"required behavior assembly was not loaded: {name}");
        }
    }
}

static void ValidateBuildOutputAssemblies(FreshBuildManifest manifest)
{
    foreach (var (fileName, expectedHash) in manifest.Assemblies)
    {
        var assemblyPath = Path.Combine(AppContext.BaseDirectory, fileName);
        if (!File.Exists(assemblyPath))
        {
            throw new InvalidOperationException($"fresh-build assembly is missing: {fileName}");
        }

        var actualHash = Sha256File(assemblyPath);
        if (!string.Equals(actualHash, expectedHash, StringComparison.Ordinal))
        {
            throw new InvalidOperationException($"fresh-build assembly hash mismatch for {fileName}: expected {expectedHash}, found {actualHash}");
        }
    }
}

static SortedDictionary<string, string> ValidateLoadedBehaviorAssemblies(FreshBuildManifest manifest)
{
    var baseDirectory = Path.GetFullPath(AppContext.BaseDirectory);
    var hashes = new SortedDictionary<string, string>(StringComparer.Ordinal);
    foreach (var assembly in AssemblyLoadContext.Default.Assemblies.OrderBy(value => value.GetName().Name, StringComparer.Ordinal))
    {
        if (string.IsNullOrWhiteSpace(assembly.Location))
        {
            continue;
        }

        var location = Path.GetFullPath(assembly.Location);
        if (!location.StartsWith(baseDirectory, StringComparison.Ordinal))
        {
            continue;
        }

        var fileName = Path.GetFileName(location);
        if (!manifest.Assemblies.TryGetValue(fileName, out var expectedHash))
        {
            throw new InvalidOperationException($"loaded behavior assembly is absent from fresh-build manifest: {fileName}");
        }

        var actualHash = Sha256File(location);
        if (!string.Equals(actualHash, expectedHash, StringComparison.Ordinal))
        {
            throw new InvalidOperationException($"loaded behavior assembly hash mismatch for {fileName}: expected {expectedHash}, found {actualHash}");
        }

        hashes.Add(assembly.GetName().Name ?? fileName, actualHash);
    }

    return hashes;
}

static SortedDictionary<string, string> BuildSourceHashes(string fixtureRoot, string upstreamRoot, string oracleSourceRoot, string manifestPath, IReadOnlyCollection<string> fixtureNames)
{
    var paths = new List<(string Name, string Path)>
    {
        ("fixture/inventory.json", Path.Combine(fixtureRoot, "inventory.json")),
        ("oracle/ProbeOracle.csproj", Path.Combine(oracleSourceRoot, "ProbeOracle.csproj")),
        ("oracle/Program.cs", Path.Combine(oracleSourceRoot, "Program.cs")),
        ("oracle/FreshBuild.targets", Path.Combine(oracleSourceRoot, "FreshBuild.targets")),
        ("oracle/MimeTypes.Nullable.cs", Path.Combine(oracleSourceRoot, "MimeTypes.Nullable.cs")),
        ("nuget/MimeTypes.Nullable.cs.pp", MimeTypesTemplatePath()),
        ("upstream/Directory.Packages.props", Path.Combine(upstreamRoot, "Directory.Packages.props")),
        ("upstream/MediaBrowser.MediaEncoding/MediaBrowser.MediaEncoding.csproj", Path.Combine(upstreamRoot, "MediaBrowser.MediaEncoding", "MediaBrowser.MediaEncoding.csproj")),
        ("upstream/MediaBrowser.Common/MediaBrowser.Common.csproj", Path.Combine(upstreamRoot, "MediaBrowser.Common", "MediaBrowser.Common.csproj")),
        ("upstream/MediaBrowser.Controller/MediaBrowser.Controller.csproj", Path.Combine(upstreamRoot, "MediaBrowser.Controller", "MediaBrowser.Controller.csproj")),
        ("upstream/MediaBrowser.Model/MediaBrowser.Model.csproj", Path.Combine(upstreamRoot, "MediaBrowser.Model", "MediaBrowser.Model.csproj")),
        ("upstream/src/Jellyfin.Extensions/Jellyfin.Extensions.csproj", Path.Combine(upstreamRoot, "src", "Jellyfin.Extensions", "Jellyfin.Extensions.csproj")),
        ("upstream/tests/Jellyfin.MediaEncoding.Tests/Probing/ProbeResultNormalizerTests.cs", Path.Combine(upstreamRoot, "tests", "Jellyfin.MediaEncoding.Tests", "Probing", "ProbeResultNormalizerTests.cs"))
    };
    paths.AddRange(Directory.EnumerateFiles(Path.Combine(upstreamRoot, "MediaBrowser.MediaEncoding", "Probing"), "*.cs", SearchOption.TopDirectoryOnly)
        .OrderBy(path => path, StringComparer.Ordinal)
        .Select(path => ($"upstream/MediaBrowser.MediaEncoding/Probing/{Path.GetFileName(path)}", path)));
    paths.AddRange(fixtureNames.Select(name => ($"fixture/{name}", Path.Combine(fixtureRoot, name))));

    paths.AddRange(Directory.EnumerateFiles(Path.Combine(upstreamRoot, "src", "Jellyfin.Extensions", "Json"), "*.cs", SearchOption.AllDirectories)
        .OrderBy(path => path, StringComparer.Ordinal)
        .Select(path => ($"upstream/src/Jellyfin.Extensions/Json/{Path.GetRelativePath(Path.Combine(upstreamRoot, "src", "Jellyfin.Extensions", "Json"), path).Replace(Path.DirectorySeparatorChar, '/')}", path)));
    var hashes = new SortedDictionary<string, string>(StringComparer.Ordinal);
    foreach (var (name, path) in paths)
    {
        if (!File.Exists(path))
        {
            throw new FileNotFoundException($"required source input is missing: {name}", path);
        }
        hashes.Add(name, Sha256File(path));
    }

    return hashes;
}

static string MimeTypesTemplatePath()
{
    var packageRoot = Environment.GetEnvironmentVariable("NUGET_PACKAGES");
    if (string.IsNullOrWhiteSpace(packageRoot))
    {
        packageRoot = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), ".nuget", "packages");
    }

    return Path.Combine(packageRoot, "mimetypes", "2.5.2", "contentFiles", "cs", "net5.0", "MimeTypes.Nullable.cs.pp");
}

static string Serialize<T>(T value, JsonSerializerOptions options)
    => JsonSerializer.Serialize(value, options).ReplaceLineEndings("\n") + "\n";

static byte[] Utf8(string value) => new UTF8Encoding(encoderShouldEmitUTF8Identifier: false).GetBytes(value);

static string Sha256File(string path) => Sha256Hex(File.ReadAllBytes(path));

static string Sha256Hex(ReadOnlySpan<byte> bytes) => Convert.ToHexStringLower(SHA256.HashData(bytes));
file class EchoLocalizationManager : DispatchProxy
{
    protected override object? Invoke(MethodInfo? targetMethod, object?[]? arguments)
    {
        if (targetMethod?.ReturnType == typeof(string))
        {
            return arguments?.OfType<string>().FirstOrDefault() ?? string.Empty;
        }

        return targetMethod?.ReturnType.IsValueType == true ? Activator.CreateInstance(targetMethod.ReturnType) : null;
    }
}

file sealed record FreshBuildManifest(string BuildRoot, SortedDictionary<string, string> Assemblies);

file sealed record Provenance(
    [property: JsonPropertyName("source_commit")] string SourceCommit,
    [property: JsonPropertyName("source_head")] string SourceHead,
    [property: JsonPropertyName("source_hashes")] SortedDictionary<string, string> SourceHashes,
    [property: JsonPropertyName("fresh_build_assembly_hashes")] SortedDictionary<string, string> FreshBuildAssemblyHashes,
    [property: JsonPropertyName("loaded_behavior_assembly_hashes")] SortedDictionary<string, string> LoadedBehaviorAssemblyHashes,
    [property: JsonPropertyName("command")] string Command,
    [property: JsonPropertyName("runtime")] string Runtime,
    [property: JsonPropertyName("serialization")] string Serialization,
    [property: JsonPropertyName("fixture_paths")] SortedDictionary<string, string> FixturePaths,
    [property: JsonPropertyName("output_hashes")] Dictionary<string, string> OutputHashes);
