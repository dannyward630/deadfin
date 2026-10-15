// SPDX-License-Identifier: GPL-2.0-only

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

var loginTestServer = config.ServerIdentity{
	ServerName: "login-test-server",
	ID:         dto.UUID{0x5d, 0x95, 0x92, 0xac, 0x6e, 0x58, 0x48, 0xbb, 0x8d, 0x75, 0x33, 0x6d, 0xf8, 0xda, 0x23, 0x01},
	Version:    "10.10.10-login-test",
}

var (
	_ auth.LoginUsers    = (*loginUsers)(nil)
	_ auth.LoginPolicy   = (*fakeLoginPolicy)(nil)
	_ auth.LoginNetwork  = (*fakeLoginNetwork)(nil)
	_ auth.LoginSessions = (*fakeLoginSessions)(nil)
)

func TestLoginRequiresClientDeviceMetadataBeforeCredentialLookup(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*auth.LoginRequest)
	}{
		{name: "missing client", mutate: func(request *auth.LoginRequest) { request.Client = "" }},
		{name: "missing device ID", mutate: func(request *auth.LoginRequest) { request.DeviceID = "" }},
		{name: "missing device name", mutate: func(request *auth.LoginRequest) { request.Device = "" }},
		{name: "missing application version", mutate: func(request *auth.LoginRequest) { request.Version = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users, _, _ := openLoginUsers(t, deterministicEntropy())
			sessions := &fakeLoginSessions{}
			login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
			request := validLoginRequest()
			test.mutate(&request)

			_, err := login.Authenticate(t.Context(), request)
			assertLoginStatus(t, err, http.StatusBadRequest)
			if users.getCalls != 0 || users.verifyCalls != 0 || users.issueCalls != 0 || sessions.checkCalls != 0 || len(sessions.logged) != 0 {
				t.Fatalf("metadata rejection side effects = get:%d verify:%d issue:%d check:%d sessions:%d; want none", users.getCalls, users.verifyCalls, users.issueCalls, sessions.checkCalls, len(sessions.logged))
			}
		})
	}
}

func TestLoginMapsUnknownAndWrongCredentialsToUnauthorizedWithoutState(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	if _, err := service.Create(t.Context(), "known-user", "correct password"); err != nil {
		t.Fatalf("create known user: %v", err)
	}

	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "unknown username", username: "absent-user", password: "correct password"},
		{name: "wrong password", username: "known-user", password: "incorrect password"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users.resetCalls()
			sessions := &fakeLoginSessions{}
			login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
			request := validLoginRequest()
			request.Username = test.username
			request.Password = test.password

			_, err := login.Authenticate(t.Context(), request)
			assertLoginStatus(t, err, http.StatusUnauthorized)
			if users.issueCalls != 0 || sessions.checkCalls != 0 || len(sessions.logged) != 0 {
				t.Fatalf("credential rejection side effects = issue:%d check:%d sessions:%d; want no token or session state", users.issueCalls, sessions.checkCalls, len(sessions.logged))
			}
		})
	}
}

func TestLoginMapsVerifiedAdmissionDenialsToForbiddenWithoutState(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	if _, err := service.Create(t.Context(), "admission-user", "correct password"); err != nil {
		t.Fatalf("create admission user: %v", err)
	}

	tests := []struct {
		name       string
		policyErr  error
		deviceErr  error
		sessionErr error
	}{
		{name: "disabled account policy", policyErr: auth.ErrLoginDenied},
		{name: "remote access policy", policyErr: auth.ErrLoginDenied},
		{name: "parental schedule policy", policyErr: auth.ErrLoginDenied},
		{name: "device access policy", deviceErr: auth.ErrLoginDenied},
		{name: "maximum active sessions", sessionErr: auth.ErrLoginDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users.resetCalls()
			sessions := &fakeLoginSessions{checkErr: test.sessionErr}
			login := newLogin(users, &fakeLoginPolicy{authorizeErr: test.policyErr, deviceErr: test.deviceErr}, &fakeLoginNetwork{local: false}, sessions)

			_, err := login.Authenticate(t.Context(), validLoginRequest())
			assertLoginStatus(t, err, http.StatusForbidden)
			if users.issueCalls != 0 || len(sessions.logged) != 0 {
				t.Fatalf("verified denial side effects = issue:%d sessions:%d; want no token or logged session", users.issueCalls, len(sessions.logged))
			}
		})
	}
}

func TestLoginDistinguishesWrongPasswordFromVerifiedDisabledUser(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	disabled := createLoginUser(t, service, "GoldenrecDisabled")
	disabled.Policy = []byte(`{"IsDisabled":true}`)
	if err := service.Update(t.Context(), disabled); err != nil {
		t.Fatalf("disable Goldenrec user: %v", err)
	}

	for _, test := range []struct {
		name     string
		password string
		want     int
	}{
		{name: "wrong password remains unauthorized", password: "wrong password", want: http.StatusUnauthorized},
		{name: "verified disabled password is forbidden", password: "correct password", want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			users.resetCalls()
			sessions := &fakeLoginSessions{}
			login := newLogin(users, disabledAwareLoginPolicy{}, &fakeLoginNetwork{}, sessions)
			request := validLoginRequest()
			request.Username = "GoldenrecDisabled"
			request.Password = test.password

			_, err := login.Authenticate(t.Context(), request)
			assertLoginStatus(t, err, test.want)
			if users.issueCalls != 0 || len(sessions.logged) != 0 {
				t.Fatalf("disabled-login side effects = issue:%d sessions:%d; want no token or session", users.issueCalls, len(sessions.logged))
			}
		})
	}
}

func TestLoginReplacesDeviceTokenLogsSessionAndReturnsAuthenticationResult(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	created, err := service.Create(t.Context(), "session-user", "correct password")
	if err != nil {
		t.Fatalf("create session user: %v", err)
	}
	old, err := service.IssueToken(t.Context(), store.Device{
		UserID:     created.ID,
		AppName:    "old-client",
		AppVersion: "old-version",
		DeviceName: "old-device",
		DeviceID:   "login-device-id",
	})
	if err != nil {
		t.Fatalf("seed old device token: %v", err)
	}
	sessions := &fakeLoginSessions{result: session.Snapshot{ID: "session-id", UserID: created.ID, Client: "test-client", DeviceID: "login-device-id", DeviceName: "test-device", Version: "test-version"}}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
	request := validLoginRequest()
	request.Username = "session-user"
	result, err := login.Authenticate(t.Context(), request)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if result.User.ID != created.ID || result.User.Name != "session-user" || result.ServerID != loginTestServer.ID.String() {
		t.Fatalf("authentication result user/server = %#v; want user %s/session-user and server %s", result, created.ID, loginTestServer.ID)
	}
	if result.AccessToken == "" || result.AccessToken == old.AccessToken {
		t.Fatalf("access token = %q; want a new token replacing %q", result.AccessToken, old.AccessToken)
	}
	if result.Session.ID != sessions.result.ID || result.Session.UserID != sessions.result.UserID || result.Session.Client != sessions.result.Client || result.Session.DeviceID != sessions.result.DeviceID || result.Session.DeviceName != sessions.result.DeviceName || result.Session.Version != sessions.result.Version {
		t.Fatalf("session result = %#v, want logged session %#v", result.Session, sessions.result)
	}
	if _, err := service.Token(t.Context(), old.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old device token after login error = %v, want ErrNotFound", err)
	}
	issued, err := service.Token(t.Context(), result.AccessToken)
	if err != nil {
		t.Fatalf("new access token lookup: %v", err)
	}
	if issued.UserID != created.ID || issued.DeviceID != "login-device-id" || issued.AppName != "test-client" || issued.AppVersion != "test-version" || issued.DeviceName != "test-device" {
		t.Fatalf("issued device = %#v; want login request device metadata", issued)
	}
	if len(sessions.logged) != 1 || sessions.logged[0].UserID != created.ID || sessions.logged[0].Client != "test-client" || sessions.logged[0].DeviceID != "login-device-id" || sessions.logged[0].DeviceName != "test-device" || sessions.logged[0].Version != "test-version" {
		t.Fatalf("logged sessions = %#v; want one login session with request identity", sessions.logged)
	}
}

func TestLoginIssueFailuresKeepExistingDeviceTokenAndDoNotLogSession(t *testing.T) {
	tests := []struct {
		name  string
		users func(t *testing.T) (*loginUsers, *user.Service, store.User, store.Device)
	}{
		{
			name: "entropy failure before replacement",
			users: func(t *testing.T) (*loginUsers, *user.Service, store.User, store.Device) {
				_, seedService, identity := openLoginUsers(t, deterministicEntropy())
				created := createLoginUser(t, seedService, "entropy-user")
				old := issueLoginDevice(t, seedService, created.ID)
				failingService := user.New(identity, clock.NewFake(time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)), failingReader{})
				return &loginUsers{service: failingService}, seedService, created, old
			},
		},
		{
			name: "persistence failure before replacement",
			users: func(t *testing.T) (*loginUsers, *user.Service, store.User, store.Device) {
				users, service, _ := openLoginUsers(t, deterministicEntropy())
				created := createLoginUser(t, service, "store-user")
				old := issueLoginDevice(t, service, created.ID)
				users.issueErr = errors.New("write device token")
				return users, service, created, old
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users, service, _, old := test.users(t)
			sessions := &fakeLoginSessions{}
			login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
			request := validLoginRequest()
			if test.name == "entropy failure before replacement" {
				request.Username = "entropy-user"
			} else {
				request.Username = "store-user"
			}

			_, err := login.Authenticate(t.Context(), request)
			assertLoginStatus(t, err, http.StatusInternalServerError)
			if _, err := service.Token(t.Context(), old.AccessToken); err != nil {
				t.Fatalf("existing token after issue failure: %v", err)
			}
			if len(sessions.logged) != 0 {
				t.Fatalf("sessions after issue failure = %#v; want none", sessions.logged)
			}
		})
	}
}

func TestLoginActivityFailureRevokesNewTokenAndCreatesNoSession(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	created := createLoginUser(t, service, "activity-failure-user")
	sessions := &fakeLoginSessions{logErr: errors.New("record session activity")}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
	request := validLoginRequest()
	request.Username = created.Name
	request.Password = "correct password"

	_, err := login.Authenticate(t.Context(), request)
	assertLoginStatus(t, err, http.StatusInternalServerError)
	if len(users.issued) != 1 {
		t.Fatalf("tokens minted before activity failure = %#v, want exactly one token to verify revocation", users.issued)
	}
	if _, err := service.Token(t.Context(), users.issued[0].AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token minted before activity failure is still usable: %v; want ErrNotFound", err)
	}
	if len(sessions.logged) != 0 {
		t.Fatalf("sessions after activity failure = %#v; want none", sessions.logged)
	}
}

func TestLoginPropagatesContextCancellationWithoutState(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	if _, err := service.Create(t.Context(), "cancelled-user", "correct password"); err != nil {
		t.Fatalf("create cancelled user: %v", err)
	}
	sessions := &fakeLoginSessions{}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := validLoginRequest()
	request.Username = "cancelled-user"

	_, err := login.Authenticate(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled login error = %v, want context.Canceled", err)
	}
	if users.issueCalls != 0 || sessions.checkCalls != 0 || len(sessions.logged) != 0 {
		t.Fatalf("cancelled login side effects = issue:%d check:%d sessions:%d; want none", users.issueCalls, sessions.checkCalls, len(sessions.logged))
	}
}

func TestDirectAuthValidatesClientDeviceMetadataBeforeUserResolution(t *testing.T) {
	users, _, _ := openLoginUsers(t, deterministicEntropy())
	sessions := &fakeLoginSessions{}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)
	tests := []struct {
		name   string
		mutate func(*auth.DirectLoginRequest)
	}{
		{name: "missing client", mutate: func(r *auth.DirectLoginRequest) { r.Client = "" }},
		{name: "missing device ID", mutate: func(r *auth.DirectLoginRequest) { r.DeviceID = "" }},
		{name: "missing device name", mutate: func(r *auth.DirectLoginRequest) { r.Device = "" }},
		{name: "missing version", mutate: func(r *auth.DirectLoginRequest) { r.Version = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users.resetCalls()
			request := validDirectLoginRequest(dto.UUID{0x01})
			test.mutate(&request)
			_, err := login.AuthenticateDirect(t.Context(), request)
			assertLoginStatus(t, err, http.StatusBadRequest)
			if users.directGetCalls != 0 || users.issueCalls != 0 || sessions.checkCalls != 0 || len(sessions.logged) != 0 {
				t.Fatalf("metadata rejection side effects = get:%d issue:%d check:%d sessions:%d; want none", users.directGetCalls, users.issueCalls, sessions.checkCalls, len(sessions.logged))
			}
		})
	}
}

func TestDirectAuthResolvesTargetByUUIDAndIssuesTokenWithoutPasswordVerification(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	target := createLoginUser(t, service, "direct-target")
	sessions := &fakeLoginSessions{}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)

	result, err := login.AuthenticateDirect(t.Context(), validDirectLoginRequest(target.ID))
	if err != nil {
		t.Fatalf("AuthenticateDirect: %v", err)
	}
	if result.User.ID != target.ID {
		t.Fatalf("result user ID = %v, want %v", result.User.ID, target.ID)
	}
	if result.AccessToken == "" {
		t.Fatal("AccessToken is empty")
	}
	if result.ServerID != loginTestServer.ID.String() {
		t.Fatalf("ServerID = %q, want %q", result.ServerID, loginTestServer.ID.String())
	}
	if users.verifyCalls != 0 {
		t.Fatalf("VerifyPassword called %d times; want 0 (direct auth bypasses password)", users.verifyCalls)
	}
	if users.getCalls != 0 {
		t.Fatalf("GetByName called %d times; want 0 (direct auth resolves by UUID)", users.getCalls)
	}
	if users.directGetCalls != 1 {
		t.Fatalf("Get called %d times; want 1", users.directGetCalls)
	}
	if users.issueCalls != 1 {
		t.Fatalf("IssueToken called %d times; want 1", users.issueCalls)
	}
	if len(sessions.logged) != 1 {
		t.Fatalf("LogActivity called %d times; want 1", len(sessions.logged))
	}
	if sessions.logged[0].UserID != target.ID {
		t.Fatalf("logged UserID = %v, want %v", sessions.logged[0].UserID, target.ID)
	}
}

func TestDirectAuthAppliesDeviceAuthorization(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	target := createLoginUser(t, service, "device-target")
	sessions := &fakeLoginSessions{}
	policy := &fakeLoginPolicy{deviceErr: auth.ErrLoginDenied}
	login := newLogin(users, policy, &fakeLoginNetwork{}, sessions)

	_, err := login.AuthenticateDirect(t.Context(), validDirectLoginRequest(target.ID))
	assertLoginStatus(t, err, http.StatusForbidden)
	if users.issueCalls != 0 || sessions.checkCalls != 0 || len(sessions.logged) != 0 {
		t.Fatalf("device-rejection side effects = issue:%d check:%d sessions:%d; want none", users.issueCalls, sessions.checkCalls, len(sessions.logged))
	}
}

func TestDirectAuthAppliesDeviceScopedSessionLimit(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	target := createLoginUser(t, service, "limit-target")
	sessions := &deviceScopedSessions{checkErr: auth.ErrLoginDenied}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)

	_, err := login.AuthenticateDirect(t.Context(), validDirectLoginRequest(target.ID))
	assertLoginStatus(t, err, http.StatusForbidden)
	if sessions.checkCalls != 1 {
		t.Fatalf("CheckLimitFor called %d times; want 1", sessions.checkCalls)
	}
	if users.issueCalls != 0 {
		t.Fatalf("IssueToken called %d times; want 0 (limit rejected before token issuance)", users.issueCalls)
	}
}

func TestDirectAuthRevokesTokenWhenActivityLoggingFails(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	target := createLoginUser(t, service, "rollback-target")
	sessions := &fakeLoginSessions{logErr: errors.New("activity log failure")}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)

	_, err := login.AuthenticateDirect(t.Context(), validDirectLoginRequest(target.ID))
	assertLoginStatus(t, err, http.StatusInternalServerError)
	if users.issueCalls != 1 {
		t.Fatalf("IssueToken called %d times; want 1 (token issued before activity log)", users.issueCalls)
	}
	if len(sessions.logged) != 0 {
		t.Fatalf("sessions after failure = %#v; want none", sessions.logged)
	}
	if len(users.issued) != 1 {
		t.Fatalf("issued tokens = %d; want 1", len(users.issued))
	}
	_, resolveErr := service.Token(t.Context(), users.issued[0].AccessToken)
	if resolveErr == nil {
		t.Fatal("token was not revoked after activity log failure")
	}
}

func TestDirectAuthUnknownUserReturnsNotFound(t *testing.T) {
	users, _, _ := openLoginUsers(t, deterministicEntropy())
	sessions := &fakeLoginSessions{}
	login := newLogin(users, &fakeLoginPolicy{}, &fakeLoginNetwork{}, sessions)

	_, err := login.AuthenticateDirect(t.Context(), validDirectLoginRequest(dto.UUID{0x99}))
	assertLoginStatus(t, err, http.StatusNotFound)
	if users.issueCalls != 0 || sessions.checkCalls != 0 || len(sessions.logged) != 0 {
		t.Fatalf("unknown-user side effects = issue:%d check:%d sessions:%d; want none", users.issueCalls, sessions.checkCalls, len(sessions.logged))
	}
}

func TestDirectAuthDoesNotRequireNetworkOrAuthorize(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	target := createLoginUser(t, service, "no-network-target")
	sessions := &fakeLoginSessions{}
	login := auth.NewLogin(auth.LoginConfig{
		Users:    users,
		Policy:   &fakeLoginPolicy{},
		Sessions: sessions,
		Server:   loginTestServer,
	})
	_, err := login.AuthenticateDirect(t.Context(), validDirectLoginRequest(target.ID))
	if err != nil {
		t.Fatalf("AuthenticateDirect without network: %v", err)
	}
}

func validDirectLoginRequest(userID dto.UUID) auth.DirectLoginRequest {
	return auth.DirectLoginRequest{
		UserID:   userID,
		Client:   "test-client",
		DeviceID: "direct-device-id",
		Device:   "test-device",
		Version:  "test-version",
	}
}

type deviceScopedSessions struct {
	checkErr   error
	checkCalls int
	result     session.Snapshot
}

func (s *deviceScopedSessions) CheckLimitFor(_ context.Context, _ dto.UUID, _, _ string) error {
	s.checkCalls++
	return s.checkErr
}

func (s *deviceScopedSessions) CheckLimit(context.Context, dto.UUID) error { return nil }

func (s *deviceScopedSessions) LogActivity(_ context.Context, activity session.Activity) (session.Snapshot, error) {
	if s.result.ID == "" {
		s.result = session.Snapshot{ID: "fake-session", UserID: activity.UserID, Client: activity.Client, DeviceID: activity.DeviceID, DeviceName: activity.DeviceName, Version: activity.Version}
	}
	return s.result, nil
}

func newLogin(users auth.LoginUsers, policy auth.LoginPolicy, network auth.LoginNetwork, sessions auth.LoginSessions) *auth.Login {
	return auth.NewLogin(auth.LoginConfig{
		Users:    users,
		Policy:   policy,
		Network:  network,
		Sessions: sessions,
		Server:   loginTestServer,
	})
}

func validLoginRequest() auth.LoginRequest {
	return auth.LoginRequest{
		Username: "admission-user",
		Password: "correct password",
		Client:   "test-client",
		DeviceID: "login-device-id",
		Device:   "test-device",
		Version:  "test-version",
		RemoteIP: netip.MustParseAddr("203.0.113.24"),
	}
}

func assertLoginStatus(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("login error = nil, want HTTP status %d", want)
	}
	var loginErr *auth.LoginError
	if !errors.As(err, &loginErr) {
		t.Fatalf("login error = %T %v, want *auth.LoginError status %d", err, err, want)
	}
	if loginErr.Status != want {
		t.Fatalf("login status = %d, want %d (error %v)", loginErr.Status, want, err)
	}
}

func openLoginUsers(t *testing.T, entropy io.Reader) (*loginUsers, *user.Service, *store.Identity) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite identity store: %v", err)
		}
	})
	identity := store.NewIdentity(stores)
	service := user.New(identity, clock.NewFake(time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)), entropy)
	return &loginUsers{service: service}, service, identity
}

func createLoginUser(t *testing.T, service *user.Service, name string) store.User {
	t.Helper()
	created, err := service.Create(t.Context(), name, "correct password")
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return created
}

func issueLoginDevice(t *testing.T, service *user.Service, userID dto.UUID) store.Device {
	t.Helper()
	device, err := service.IssueToken(t.Context(), store.Device{
		UserID:     userID,
		AppName:    "old-client",
		AppVersion: "old-version",
		DeviceName: "old-device",
		DeviceID:   "login-device-id",
	})
	if err != nil {
		t.Fatalf("issue old device token: %v", err)
	}
	return device
}

func deterministicEntropy() io.Reader {
	data := make([]byte, 4096)
	for index := range data {
		data[index] = byte(index%251 + 1)
	}
	return bytes.NewReader(data)
}

type loginUsers struct {
	service        *user.Service
	issueErr       error
	getCalls       int
	directGetCalls int
	verifyCalls    int
	issueCalls     int
	issued         []store.Device
}

func (users *loginUsers) GetByName(ctx context.Context, name string) (store.User, error) {
	users.getCalls++
	if err := ctx.Err(); err != nil {
		return store.User{}, err
	}
	return users.service.GetByName(ctx, name)
}

func (users *loginUsers) Get(ctx context.Context, id dto.UUID) (store.User, error) {
	users.directGetCalls++
	if err := ctx.Err(); err != nil {
		return store.User{}, err
	}
	return users.service.Get(ctx, id)
}

func (users *loginUsers) VerifyPassword(ctx context.Context, id dto.UUID, password string) (bool, error) {
	users.verifyCalls++
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return users.service.VerifyPassword(ctx, id, password)
}

func (users *loginUsers) RecordSuccessfulLogin(ctx context.Context, id dto.UUID) error {
	return users.service.RecordSuccessfulLogin(ctx, id)
}

func (users *loginUsers) IssueToken(ctx context.Context, device store.Device) (store.Device, error) {
	users.issueCalls++
	if err := ctx.Err(); err != nil {
		return store.Device{}, err
	}
	if users.issueErr != nil {
		return store.Device{}, users.issueErr
	}
	issued, err := users.service.IssueToken(ctx, device)
	if err != nil {
		return store.Device{}, err
	}
	users.issued = append(users.issued, issued)
	return issued, nil
}

func (users *loginUsers) Logout(ctx context.Context, token string) error {
	return users.service.Logout(ctx, token)
}

func (users *loginUsers) resetCalls() {
	users.getCalls = 0
	users.directGetCalls = 0
	users.verifyCalls = 0
	users.issueCalls = 0
}

type fakeLoginPolicy struct {
	authorizeErr error
	deviceErr    error
}

func (policy *fakeLoginPolicy) Authorize(_ context.Context, _ store.User, _ netip.Addr, _ bool) error {
	return policy.authorizeErr
}

func (policy *fakeLoginPolicy) AuthorizeDevice(_ context.Context, _ store.User, _ string) error {
	return policy.deviceErr
}

// disabledAwareLoginPolicy is the admission-policy seam used by this login
// test. It consumes the persisted policy document just as the production
// authorization evaluator does for a disabled account.
type disabledAwareLoginPolicy struct{}

func (disabledAwareLoginPolicy) Authorize(_ context.Context, user store.User, _ netip.Addr, _ bool) error {
	var policy struct {
		IsDisabled bool
	}
	if err := json.Unmarshal(user.Policy, &policy); err != nil {
		return err
	}
	if policy.IsDisabled {
		return auth.ErrLoginDenied
	}
	return nil
}

func (disabledAwareLoginPolicy) AuthorizeDevice(context.Context, store.User, string) error {
	return nil
}

type fakeLoginNetwork struct{ local bool }

func (network *fakeLoginNetwork) IsLocal(_ netip.Addr) bool { return network.local }

type fakeLoginSessions struct {
	checkErr   error
	checkCalls int
	logErr     error
	logged     []session.Activity
	result     session.Snapshot
}

func (sessions *fakeLoginSessions) CheckLimit(_ context.Context, _ dto.UUID) error {
	sessions.checkCalls++
	return sessions.checkErr
}

func (sessions *fakeLoginSessions) LogActivity(_ context.Context, activity session.Activity) (session.Snapshot, error) {
	if sessions.logErr != nil {
		return session.Snapshot{}, sessions.logErr
	}
	sessions.logged = append(sessions.logged, activity)
	if sessions.result.ID == "" {
		sessions.result = session.Snapshot{
			ID:         "fake-session",
			UserID:     activity.UserID,
			Client:     activity.Client,
			DeviceID:   activity.DeviceID,
			DeviceName: activity.DeviceName,
			Version:    activity.Version,
		}
	}
	return sessions.result, nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
