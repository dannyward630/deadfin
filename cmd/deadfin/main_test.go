// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"

	"github.com/deadfin-org/deadfin/internal/config"
)

const routesCommandTimeout = 10 * time.Second

func TestRoutesJSONCommandWritesValidJSON(t *testing.T) {
	output := runDeadfinRoutes(t, "routes", "--json")
	if !json.Valid(output) {
		t.Fatalf("routes --json produced invalid JSON: %q", output)
	}
}

func TestRoutesJSONCommandIsDeterministic(t *testing.T) {
	first := runDeadfinRoutes(t, "routes", "--json")
	second := runDeadfinRoutes(t, "routes", "--json")
	if !bytes.Equal(first, second) {
		t.Fatalf("routes --json output was not deterministic:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestRoutesJSONCommandDoesNotStartListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close occupied listener: %v", err)
		}
	})

	output := runDeadfinRoutes(t, "--listen", listener.Addr().String(), "routes", "--json")
	if !json.Valid(output) {
		t.Fatalf("routes --json with an occupied listen address produced invalid JSON: %q", output)
	}
}

func TestRoutesJSONCommandFaithfullyExportsRegisteredMetadata(t *testing.T) {
	output := runDeadfinRoutes(t, "routes", "--json")
	want, err := json.Marshal(newRouter(config.DefaultServerIdentity()).Routes())
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if !bytes.Equal(output, want) {
		t.Fatalf("routes --json did not faithfully export registered route metadata:\nwant: %s\ngot:  %s", want, output)
	}
}

func TestMainDispatchesFrameSinkTrampolineBeforeFlagParsing(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), routesCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeadfinFrameSinkTrampolineHelper$")
	cmd.Env = append(os.Environ(),
		"DEADFIN_TEST_FRAME_SINK_TRAMPOLINE_HELPER=1",
		"DEADFIN_FRAME_SINK_FD=not-a-frame-sink-fd",
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("frame sink trampoline helper did not return: %v\noutput: %s", ctx.Err(), output)
	}
	if err == nil {
		t.Fatalf("frame sink trampoline helper succeeded; output: %s", output)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 125 {
		t.Fatalf("frame sink trampoline helper exit = %v, want status 125 before flag parsing; output: %s", err, output)
	}
	if !strings.Contains(string(output), "deadfin frame sink trampoline: invalid frame sink fd") {
		t.Fatalf("frame sink trampoline helper output = %q, want trampoline rejection", output)
	}
}

// TestRunServerRegistersOwnershipDefersInUnwindOrder locks the process-entry
// ownership contract at its narrowest stable seam. The defer registration
// order proves every successful composer construction unwinds app work first,
// then composer, then the process-wide runtime; locating the composer defer
// before ffmpeg probing also covers post-construction failures.
func TestRunServerRegistersOwnershipDefersInUnwindOrder(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read runServer source: %v", err)
	}
	contents := string(source)
	runtimeClose := "defer func() { err = errors.Join(err, imageRuntime.Close()) }()"
	composerClose := "defer func() { err = errors.Join(err, tileComposer.Close()) }()"
	probe := "ffmpeg.ProbeCapabilities(ctx, \"ffmpeg\")"
	constructApp := "newApplicationWithListen(ctx, programData"
	closeApp := "defer func() { err = errors.Join(err, closeApplicationWithRetry(app)) }()"
	positions := []struct {
		name string
		at   int
	}{
		{"runtime close", strings.Index(contents, runtimeClose)},
		{"composer close", strings.Index(contents, composerClose)},
		{"ffmpeg probe", strings.Index(contents, probe)},
		{"application construction", strings.Index(contents, constructApp)},
		{"application close", strings.Index(contents, closeApp)},
	}
	for _, position := range positions {
		if position.at < 0 {
			t.Fatalf("runServer is missing %s ownership boundary", position.name)
		}
	}
	for index := 1; index < len(positions); index++ {
		if positions[index-1].at >= positions[index].at {
			t.Fatalf("runServer ownership order = %s before %s at source offsets %d >= %d; want runtime defer, composer defer, construction, app defer", positions[index-1].name, positions[index].name, positions[index-1].at, positions[index].at)
		}
	}
}

func runDeadfinRoutes(t *testing.T, args ...string) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), routesCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeadfinRoutesJSONHelper$")
	cmd.Env = append(os.Environ(), "DEADFIN_TEST_HELPER=1", "DEADFIN_TEST_ARGS="+strings.Join(args, " "))
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("deadfin %s did not return; routes export must not start a server: %v\noutput: %s", strings.Join(args, " "), ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("deadfin %s: %v\noutput: %s", strings.Join(args, " "), err, output)
	}
	return output
}

func TestDeadfinRoutesJSONHelper(t *testing.T) {
	if os.Getenv("DEADFIN_TEST_HELPER") != "1" {
		return
	}

	os.Args = append([]string{"deadfin"}, strings.Fields(os.Getenv("DEADFIN_TEST_ARGS"))...)
	main()
	os.Exit(0)
}

func TestDeadfinFrameSinkTrampolineHelper(t *testing.T) {
	if os.Getenv("DEADFIN_TEST_FRAME_SINK_TRAMPOLINE_HELPER") != "1" {
		return
	}

	os.Args = []string{"deadfin", "-flag-must-not-be-parsed-before-frame-sink-trampoline"}
	main()
	t.Fatal("main returned without dispatching the frame sink trampoline")
}

// writeNetworkXML writes a network.xml with the given EnableRemoteAccess and
// AutoDiscovery values into a fresh program-data directory. It returns the
// program-data path.
func writeNetworkXML(t *testing.T, autoDiscovery bool) string {
	t.Helper()
	programData := t.TempDir()
	configDir := filepath.Join(programData, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	// runServer creates a tile composer that requires the trickplay root
	// directory to exist.
	if err := os.MkdirAll(filepath.Join(programData, "data", "trickplay"), 0o700); err != nil {
		t.Fatalf("create trickplay dir: %v", err)
	}
	networkXML := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<NetworkConfiguration>
  <EnableRemoteAccess>true</EnableRemoteAccess>
  <AutoDiscovery>%t</AutoDiscovery>
</NetworkConfiguration>`, autoDiscovery)
	if err := os.WriteFile(filepath.Join(configDir, "network.xml"), []byte(networkXML), 0o600); err != nil {
		t.Fatalf("write network.xml: %v", err)
	}
	return programData
}

// startServer runs runServer in a goroutine on a free TCP port and returns
// the actual address, a done channel, and a cancel function. It uses the
// readiness channel instead of polling.
func startServer(t *testing.T, programData string) (addr string, done <-chan error, cancel func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- runServer(ctx, "127.0.0.1:0", programData, ready)
	}()
	select {
	case a := <-ready:
		return a, doneCh, cancel
	case err := <-doneCh:
		cancel()
		t.Fatalf("runServer exited before readiness: %v", err)
		return "", nil, nil
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatalf("runServer did not signal readiness")
		return "", nil, nil
	}
}

// TestAutoDiscoveryBindFailureLogsAndContinuesServing verifies that when
// AutoDiscovery is enabled and UDP 7359 is occupied, runServer logs the
// bind failure and continues to serve HTTP requests.
func TestAutoDiscoveryBindFailureLogsAndContinuesServing(t *testing.T) {
	occupier, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 7359})
	if err != nil {
		t.Skipf("UDP 7359 is not available: %v", err)
	}
	defer occupier.Close()

	programData := writeNetworkXML(t, true)

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	addr, done, cancel := startServer(t, programData)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("runServer did not exit after cancel")
		}
	}()

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "discovery:") || !strings.Contains(logOutput, "bind") {
		t.Fatalf("The log must contain the discovery bind failure. The log is: %s", logOutput)
	}
	if strings.Contains(logOutput, "discovery: discovery:") {
		t.Fatalf("The log must not contain a duplicate discovery label. The log is: %s", logOutput)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/System/Ping", addr))
	if err != nil {
		t.Fatalf("System/Ping: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("System/Ping status = %d, want 200", resp.StatusCode)
	}
}

// TestAutoDiscoveryDisabledDoesNotBind verifies that when AutoDiscovery is
// disabled, runServer does not attempt to bind UDP 7359. The test runs in a
// subprocess to get its own image runtime. The parent occupies UDP 7359. The
// subprocess starts with AutoDiscovery=false and must not log a discovery
// bind attempt.
func TestAutoDiscoveryDisabledDoesNotBind(t *testing.T) {
	occupier, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 7359})
	if err != nil {
		t.Skipf("UDP 7359 is not available: %v", err)
	}
	defer occupier.Close()

	programData := writeNetworkXML(t, false)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoDiscoveryDisabledHelper$")
	cmd.Env = append(os.Environ(),
		"DEADFIN_DISABLED_HELPER=1",
		"DEADFIN_PROGRAM_DATA="+programData,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		t.Fatalf("subprocess failed: %v\noutput: %s", err, stdout.String())
	}

	output := stdout.String()
	if strings.Contains(output, "discovery:") {
		t.Fatalf("Disabled discovery must not log a bind attempt. The log is: %s", output)
	}
	if !strings.Contains(output, "DISABLED_HELPER_OK") {
		t.Fatalf("The subprocess failed. The output is: %s", output)
	}
}

// TestAutoDiscoveryDisabledHelper is the subprocess entry point for
// TestAutoDiscoveryDisabledDoesNotBind. It starts runServer with
// AutoDiscovery=false and a pre-occupied UDP 7359, verifies HTTP responds,
// then prints DISABLED_HELPER_OK and exits.
func TestAutoDiscoveryDisabledHelper(t *testing.T) {
	if os.Getenv("DEADFIN_DISABLED_HELPER") != "1" {
		t.Skip()
	}

	programData := os.Getenv("DEADFIN_PROGRAM_DATA")
	if programData == "" {
		t.Fatal("DEADFIN_PROGRAM_DATA not set")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, "127.0.0.1:0", programData, ready)
	}()

	var addr string
	select {
	case a := <-ready:
		addr = a
	case err := <-done:
		t.Fatalf("runServer exited before readiness: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("runServer did not signal readiness")
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/System/Ping", addr))
	if err != nil {
		t.Fatalf("System/Ping: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("System/Ping status = %d, want 200", resp.StatusCode)
	}

	cancel()
	<-done
	fmt.Println("DISABLED_HELPER_OK")
}

// TestAutoDiscoveryIdentityMatchesHTTP proves the UDP discovery responder
// advertises the same persisted installation identity as /System/Info/Public.
// The test runs in a subprocess to get its own image runtime. The subprocess
// starts Deadfin with BaseUrl=/jf and AutoDiscovery=true, queries the HTTP
// endpoint, sends a UDP discovery datagram, and prints both identities. The
// parent compares the pairs.
func TestAutoDiscoveryIdentityMatchesHTTP(t *testing.T) {
	// The discovery responder binds UDP 7359. Skip if the port is in use.
	occupier, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 7359})
	if err != nil {
		t.Skipf("UDP 7359 is not available: %v", err)
	}
	if err := occupier.Close(); err != nil {
		t.Fatalf("close UDP occupier: %v", err)
	}

	programData := t.TempDir()
	configDir := filepath.Join(programData, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(programData, "data", "trickplay"), 0o700); err != nil {
		t.Fatalf("create trickplay dir: %v", err)
	}
	networkXML := `<?xml version="1.0" encoding="utf-8"?>
<NetworkConfiguration>
  <EnableRemoteAccess>true</EnableRemoteAccess>
  <AutoDiscovery>true</AutoDiscovery>
  <BaseUrl>/jf</BaseUrl>
</NetworkConfiguration>`
	if err := os.WriteFile(filepath.Join(configDir, "network.xml"), []byte(networkXML), 0o600); err != nil {
		t.Fatalf("write network.xml: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoDiscoveryIdentityHelper$")
	cmd.Env = append(os.Environ(),
		"DEADFIN_IDENTITY_HELPER=1",
		"DEADFIN_PROGRAM_DATA="+programData,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		t.Fatalf("subprocess failed: %v\noutput: %s", err, stdout.String())
	}

	output := stdout.String()
	httpLine := extractLine(output, "HTTP_IDENTITY:")
	udpLine := extractLine(output, "UDP_IDENTITY:")
	if httpLine == "" || udpLine == "" {
		t.Fatalf("The subprocess did not print both identities. The output is: %s", output)
	}
	if httpLine != udpLine {
		t.Errorf("HTTP identity %q does not match UDP identity %q", httpLine, udpLine)
	}
}

// TestAutoDiscoveryIdentityHelper is the subprocess entry point for
// TestAutoDiscoveryIdentityMatchesHTTP. It starts runServer with
// BaseUrl=/jf and AutoDiscovery=true, queries /jf/System/Info/Public and the
// UDP discovery responder, then prints both identities as JSON lines.
func TestAutoDiscoveryIdentityHelper(t *testing.T) {
	if os.Getenv("DEADFIN_IDENTITY_HELPER") != "1" {
		t.Skip()
	}

	programData := os.Getenv("DEADFIN_PROGRAM_DATA")
	if programData == "" {
		t.Fatal("DEADFIN_PROGRAM_DATA not set")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, "127.0.0.1:0", programData, ready)
	}()

	var addr string
	select {
	case a := <-ready:
		addr = a
	case err := <-done:
		t.Fatalf("runServer exited before readiness: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("runServer did not signal readiness")
	}

	// Query the HTTP identity through the BaseUrl mount.
	httpResp, err := http.Get(fmt.Sprintf("http://%s/jf/System/Info/Public", addr))
	if err != nil {
		t.Fatalf("GET /jf/System/Info/Public: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /jf/System/Info/Public status = %d, want 200", httpResp.StatusCode)
	}
	var publicInfo struct {
		Id         string `json:"Id"`
		ServerName string `json:"ServerName"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&publicInfo); err != nil {
		t.Fatalf("decode /jf/System/Info/Public: %v", err)
	}

	// Send the UDP discovery query and read the response.
	udpResp := sendDiscoveryQuery(t, "127.0.0.1:7359")
	if len(udpResp) == 0 {
		t.Fatal("The UDP discovery query produced no response.")
	}
	var udpInfo struct {
		Address         string  `json:"Address"`
		Id              string  `json:"Id"`
		Name            string  `json:"Name"`
		EndpointAddress *string `json:"EndpointAddress"`
	}
	if err := json.Unmarshal(udpResp, &udpInfo); err != nil {
		t.Fatalf("decode UDP discovery response: %v\nresponse: %s", err, udpResp)
	}

	fmt.Printf("HTTP_IDENTITY:%s|%s\n", publicInfo.Id, publicInfo.ServerName)
	fmt.Printf("UDP_IDENTITY:%s|%s\n", udpInfo.Id, udpInfo.Name)

	cancel()
	<-done
}

// sendDiscoveryQuery sends a UDP discovery datagram and returns the response.
func sendDiscoveryQuery(t *testing.T, addr string) []byte {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		t.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		t.Fatalf("dial UDP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("who is JellyfinServer?")); err != nil {
		t.Fatalf("write discovery query: %v", err)
	}
	conn.SetReadDeadline(clock.NewReal().Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// extractLine returns the text after the prefix on the first matching line.
func extractLine(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
