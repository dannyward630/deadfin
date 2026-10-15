// SPDX-License-Identifier: GPL-2.0-only

// avprobe: play QA matrix streams through AVFoundation and report what an
// AVPlayer client sees. See Package.swift for usage.
//
// Per plan entry it measures:
//   - time to first video frame
//   - stalls (AVPlayerItemPlaybackStalled and waiting time after start)
//   - timeline continuity: item time against host time while playing, and
//     AVPlayerItemTimeJumped notifications that no seek caused
//   - decoded video frames: gaps in presentation time (AVPlayerItemVideoOutput)
//   - audio continuity: an MTAudioProcessingTap where the asset exposes
//     tracks (progressive MP4); HLS does not, so for HLS the server-side
//     fMP4 timeline check (qamatrix check) covers audio
//   - seeks of +10 s, -10 s, +5 min, -5 min and a 5 s pause, with landing
//     error and time to the next frame
//   - AVPlayerItem accessLog and errorLog entries

import AVFoundation
import CoreMedia
import Foundation
import MediaToolbox
import QuartzCore

struct PlanEntry: Codable {
    var cell: String
    var item: String
    var player: String
    var name: String
    var method: String
    var url: String
    var deviceId: String
    var playSessionId: String
    var seconds: Double
    var seeks: [Double]
    var runtimeS: Double
}

struct Output: Codable {
    var cell: String
    var item: String
    var player: String
    var result: String
    var notes: [String]
    var metrics: [String: Double]
    var accessLog: [[String: String]]
    var errorLog: [[String: String]]
    var host: String
    var when: String
}

// MARK: - Credentials

/// Removes the query string, which carries the API key, from a URL.
func redact(_ s: String?) -> String {
    guard let s else { return "" }
    if let i = s.firstIndex(of: "?") { return String(s[..<i]) + "?…" }
    return s
}

func withKey(_ raw: String, token: String) -> URL? {
    guard var c = URLComponents(string: raw) else { return nil }
    var q = c.queryItems ?? []
    q.removeAll { $0.name.lowercased() == "apikey" || $0.name.lowercased() == "api_key" }
    q.append(URLQueryItem(name: "ApiKey", value: token))
    c.queryItems = q
    return c.url
}

// MARK: - Audio tap

/// TapState counts the audio the tap renders and the gaps in its source
/// timeline. The tap runs on a real-time thread, so access is locked.
final class TapState {
    private let lock = NSLock()
    private var lastEnd = Double.nan
    private var sampleRate = 0.0
    private(set) var frames: Int64 = 0
    private(set) var gaps = 0
    private(set) var maxGap = 0.0
    private var ignoreUntil = 0.0

    func setRate(_ r: Double) { lock.lock(); sampleRate = r; lock.unlock() }

    /// Seeks and pauses reset the expected position.
    func reset(ignoreFor seconds: Double) {
        lock.lock()
        lastEnd = .nan
        ignoreUntil = CACurrentMediaTime() + seconds
        lock.unlock()
    }

    func record(start: Double, count: Int) {
        lock.lock()
        defer { lock.unlock() }
        guard sampleRate > 0, start.isFinite else { return }
        frames += Int64(count)
        let end = start + Double(count) / sampleRate
        if lastEnd.isFinite, CACurrentMediaTime() > ignoreUntil {
            let gap = abs(start - lastEnd)
            if gap > 0.025 {
                gaps += 1
                maxGap = max(maxGap, gap)
            }
        }
        lastEnd = end
    }

    var snapshot: (frames: Int64, gaps: Int, maxGap: Double) {
        lock.lock(); defer { lock.unlock() }
        return (frames, gaps, maxGap)
    }
}

func makeTap(_ state: TapState) -> MTAudioProcessingTap? {
    var callbacks = MTAudioProcessingTapCallbacks(
        version: kMTAudioProcessingTapCallbacksVersion_0,
        clientInfo: UnsafeMutableRawPointer(Unmanaged.passRetained(state).toOpaque()),
        init: { _, clientInfo, storageOut in storageOut.pointee = clientInfo },
        finalize: { tap in
            Unmanaged<TapState>.fromOpaque(MTAudioProcessingTapGetStorage(tap)).release()
        },
        prepare: { tap, _, format in
            Unmanaged<TapState>.fromOpaque(MTAudioProcessingTapGetStorage(tap)).takeUnretainedValue()
                .setRate(format.pointee.mSampleRate)
        },
        unprepare: nil,
        process: { tap, frames, _, buffers, framesOut, flagsOut in
            var range = CMTimeRange()
            let status = MTAudioProcessingTapGetSourceAudio(tap, frames, buffers, flagsOut, &range, framesOut)
            if status == noErr {
                Unmanaged<TapState>.fromOpaque(MTAudioProcessingTapGetStorage(tap)).takeUnretainedValue()
                    .record(start: range.start.seconds, count: Int(framesOut.pointee))
            }
        }
    )
    // The macOS 26+ SDKs return the tap as a managed reference; older SDKs
    // return it unmanaged.
    #if compiler(>=6.2)
    var tap: MTAudioProcessingTap?
    let status = MTAudioProcessingTapCreate(kCFAllocatorDefault, &callbacks, kMTAudioProcessingTapCreationFlag_PostEffects, &tap)
    return status == noErr ? tap : nil
    #else
    var tap: Unmanaged<MTAudioProcessingTap>?
    let status = MTAudioProcessingTapCreate(kCFAllocatorDefault, &callbacks, kMTAudioProcessingTapCreationFlag_PostEffects, &tap)
    return status == noErr ? tap?.takeRetainedValue() : nil
    #endif
}

// MARK: - Probe

@MainActor
final class Probe {
    let e: PlanEntry
    let token: String
    var notes: [String] = []
    var failures: [String] = []
    var metrics: [String: Double] = [:]

    var player: AVPlayer!
    var item: AVPlayerItem!
    var output: AVPlayerItemVideoOutput!
    var tapState: TapState?

    // Quiet windows: seeks and pauses start one; stalls, jumps and gaps
    // inside it are expected.
    var quietUntil = 0.0
    var stalls = 0
    var stallEvents = 0
    var timeJumps = 0
    var unexpectedJumps = 0
    var observers: [NSObjectProtocol] = []

    var frames = 0
    var lastPTS = Double.nan
    var frameGaps: [Double] = []
    var bigGaps = 0
    var backwards = 0
    var maxGap = 0.0

    init(_ e: PlanEntry, token: String) {
        self.e = e
        self.token = token
    }

    func fail(_ s: String) { failures.append(s) }

    func quiet(_ seconds: Double) {
        quietUntil = max(quietUntil, CACurrentMediaTime() + seconds)
        lastPTS = .nan
        tapState?.reset(ignoreFor: seconds)
    }

    var isQuiet: Bool { CACurrentMediaTime() < quietUntil }

    func run() async -> Output {
        defer { teardown() }
        guard let url = withKey(e.url, token: token) else {
            fail("bad URL")
            return finish()
        }
        let headers = ["Authorization": "MediaBrowser Client=\"avprobe\", Device=\"avprobe\", DeviceId=\"\(e.deviceId)\", Version=\"1\", Token=\"\(token)\""]
        let asset = AVURLAsset(url: url, options: ["AVURLAssetHTTPHeaderFieldsKey": headers])
        item = AVPlayerItem(asset: asset)
        output = AVPlayerItemVideoOutput(pixelBufferAttributes: nil)
        item.add(output)

        if e.method == "DirectPlay", let track = try? await asset.loadTracks(withMediaType: .audio).first {
            let state = TapState()
            if let tap = makeTap(state) {
                let params = AVMutableAudioMixInputParameters(track: track)
                params.audioTapProcessor = tap
                let mix = AVMutableAudioMix()
                mix.inputParameters = [params]
                item.audioMix = mix
                tapState = state
            }
        }

        let nc = NotificationCenter.default
        observers.append(nc.addObserver(forName: .AVPlayerItemPlaybackStalled, object: item, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.onStall() }
        })
        observers.append(nc.addObserver(forName: AVPlayerItem.timeJumpedNotification, object: item, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.onJump() }
        })

        player = AVPlayer(playerItem: item)
        player.isMuted = true

        // Start.
        let t0 = CACurrentMediaTime()
        quiet(1)
        player.play()
        var first: Double?
        while CACurrentMediaTime() - t0 < 30 {
            if item.status == .failed { break }
            pollFrames()
            if frames > 0 {
                first = CACurrentMediaTime() - t0
                break
            }
            try? await Task.sleep(nanoseconds: 8_000_000)
        }
        guard let ttff = first else {
            fail("no video frame within 30 s (status \(item.status.rawValue)): \(item.error?.localizedDescription ?? "no error")")
            return finish()
        }
        metrics["ttffS"] = round3(ttff)
        if tapState == nil { attachItemTap() }
        if ttff > 10 { fail(String(format: "slow start: first frame after %.1f s", ttff)) }

        // Play, with the operations spread over the run.
        let total = e.seconds
        var ops: [(at: Double, op: Op)] = []
        let n = Double(e.seeks.count + 1)
        for (i, s) in e.seeks.enumerated() { ops.append((total * Double(i + 1) / (n + 1), .seek(s))) }
        ops.append((total * n / (n + 1), .pause(5)))

        let start = CACurrentMediaTime()
        var lastHost = CACurrentMediaTime()
        var lastItem = item.currentTime().seconds
        var playing = 0.0, advanced = 0.0
        var nextSample = start
        var seekErrors: [Double] = [], seekResumes: [Double] = []
        while CACurrentMediaTime() - start < total {
            if item.status == .failed {
                fail("item failed: \(item.error?.localizedDescription ?? "")")
                break
            }
            if let next = ops.first, CACurrentMediaTime() - start >= next.at {
                ops.removeFirst()
                switch next.op {
                case let .seek(delta):
                    if let r = await seek(by: delta) {
                        seekErrors.append(r.error)
                        seekResumes.append(r.resume)
                    }
                case let .pause(s):
                    player.pause()
                    quiet(s + 3)
                    try? await Task.sleep(nanoseconds: UInt64(s * 1e9))
                    let t = CACurrentMediaTime()
                    let before = frames
                    player.play()
                    while frames == before, CACurrentMediaTime() - t < 15 {
                        pollFrames()
                        try? await Task.sleep(nanoseconds: 8_000_000)
                    }
                    metrics["pauseResumeS"] = round3(CACurrentMediaTime() - t)
                }
                lastHost = CACurrentMediaTime()
                lastItem = item.currentTime().seconds
                continue
            }
            pollFrames()
            let now = CACurrentMediaTime()
            if now >= nextSample {
                nextSample = now + 0.1
                let cur = item.currentTime().seconds
                let dh = now - lastHost, di = cur - lastItem
                if player.timeControlStatus == .playing, !isQuiet {
                    playing += dh
                    advanced += di
                    let drift = di - dh
                    if abs(drift) > 0.25 {
                        unexpectedJumps += 1
                        if unexpectedJumps <= 5 {
                            notes.append(String(format: "timeline jump %+.3f s at %.1f s", drift, cur))
                        }
                    }
                } else if player.timeControlStatus == .waitingToPlayAtSpecifiedRate, !isQuiet {
                    metrics["waitingS", default: 0] += dh
                }
                lastHost = now
                lastItem = cur
            }
            try? await Task.sleep(nanoseconds: 8_000_000)
        }

        // Metrics and verdict.
        metrics["playedS"] = round3(playing)
        if playing > 10 {
            let ratio = advanced / playing
            metrics["rateRatio"] = round3(ratio)
            if ratio < 0.97 || ratio > 1.03 { fail(String(format: "item time ran at %.2fx host time", ratio)) }
        }
        metrics["frames"] = Double(frames)
        metrics["frameGapEvents"] = Double(bigGaps)
        metrics["maxFrameGapS"] = round3(maxGap)
        if bigGaps > 0 { fail(String(format: "%d video gaps over 0.25 s (max %.3f s)", bigGaps, maxGap)) }
        metrics["videoBackwards"] = Double(backwards)
        if backwards > 0 { fail("video presentation time went backwards \(backwards) times") }
        metrics["stalls"] = Double(stalls)
        if stalls > 0 { fail("\(stalls) stalls outside seeks") }
        metrics["timeJumped"] = Double(timeJumps)
        metrics["timelineJumps"] = Double(unexpectedJumps)
        if unexpectedJumps > 0 { fail("\(unexpectedJumps) timeline jumps while playing") }
        if let w = metrics["waitingS"], w > 2 { fail(String(format: "waited %.1f s for data outside seeks", w)) }
        if let s = tapState?.snapshot {
            metrics["audioFrames"] = Double(s.frames)
            metrics["audioGaps"] = Double(s.gaps)
            metrics["audioMaxGapS"] = round3(s.maxGap)
            if s.frames == 0 { notes.append("audio tap saw no audio (AVFoundation ignores audioMix for HLS); audio continuity comes from qamatrix check") }
            if s.gaps > 0 { fail(String(format: "%d audio timeline gaps (max %.3f s)", s.gaps, s.maxGap)) }
        }
        if let m = seekErrors.map(abs).max() {
            metrics["seekMaxErrorS"] = round3(m)
            if m > 2 { fail(String(format: "seek landed %.1f s from target", m)) }
        }
        if let m = seekResumes.max() {
            metrics["seekMaxResumeS"] = round3(m)
            if m > 10 { fail(String(format: "seek took %.1f s to show a frame", m)) }
        }
        return finish()
    }

    enum Op { case seek(Double), pause(Double) }

    func seek(by delta: Double) async -> (error: Double, resume: Double)? {
        let cur = item.currentTime().seconds
        let target = min(max(cur + delta, 0), max(e.runtimeS - 30, 0))
        quiet(12)
        let t = CACurrentMediaTime()
        let before = frames
        // MediaPlayerProxy+AVPlayer.swift seeks with zero tolerance.
        let done = await player.seek(to: CMTime(seconds: target, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero)
        if !done {
            notes.append(String(format: "seek %+.0f s interrupted", delta))
            return nil
        }
        let landed = item.currentTime().seconds
        while frames == before, CACurrentMediaTime() - t < 20 {
            pollFrames()
            try? await Task.sleep(nanoseconds: 8_000_000)
        }
        let resume = CACurrentMediaTime() - t
        notes.append(String(format: "seek %+.0f s: landed %+.2f s from target, frame after %.2f s", delta, landed - target, resume))
        return (landed - target, resume)
    }

    /// For HLS the asset has no tracks before playback; try the player
    /// item's tracks once they exist.
    func attachItemTap() {
        guard let track = item.tracks.compactMap(\.assetTrack).first(where: { $0.mediaType == .audio }) else {
            notes.append("audio tap unavailable: no audio asset track; audio continuity comes from qamatrix check")
            return
        }
        let state = TapState()
        guard let tap = makeTap(state) else { return }
        let params = AVMutableAudioMixInputParameters(track: track)
        params.audioTapProcessor = tap
        let mix = AVMutableAudioMix()
        mix.inputParameters = [params]
        item.audioMix = mix
        tapState = state
    }

    func pollFrames() {
        let now = output.itemTime(forHostTime: CACurrentMediaTime())
        guard output.hasNewPixelBuffer(forItemTime: now) else { return }
        var shown = CMTime.invalid
        guard output.copyPixelBuffer(forItemTime: now, itemTimeForDisplay: &shown) != nil else { return }
        frames += 1
        let pts = shown.seconds
        if lastPTS.isFinite, pts.isFinite, !isQuiet, player.timeControlStatus == .playing {
            let gap = pts - lastPTS
            if gap < -0.02 {
                backwards += 1
                if backwards <= 5 { notes.append(String(format: "video time went back %.3f s at %.1f s", -gap, pts)) }
            }
            if gap > 0.25 {
                bigGaps += 1
                maxGap = max(maxGap, gap)
                if bigGaps <= 5 { notes.append(String(format: "video gap %.3f s at %.1f s", gap, pts)) }
            }
        }
        lastPTS = pts
    }

    func onStall() {
        stallEvents += 1
        if !isQuiet {
            stalls += 1
            notes.append(String(format: "stall at %.1f s", item.currentTime().seconds))
        }
    }

    func onJump() {
        timeJumps += 1
        if !isQuiet {
            unexpectedJumps += 1
            notes.append(String(format: "AVPlayerItemTimeJumped at %.1f s", item.currentTime().seconds))
        }
    }

    func finish() -> Output {
        var access: [[String: String]] = []
        for ev in item?.accessLog()?.events ?? [] {
            access.append([
                "uri": redact(ev.uri),
                "playbackType": ev.playbackType ?? "",
                "startupTime": String(format: "%.3f", ev.startupTime),
                "durationWatched": String(format: "%.1f", ev.durationWatched),
                "numberOfStalls": String(ev.numberOfStalls),
                "numberOfDroppedVideoFrames": String(ev.numberOfDroppedVideoFrames),
                "numberOfMediaRequests": String(ev.numberOfMediaRequests),
                "segmentsDownloadedDuration": String(format: "%.1f", ev.segmentsDownloadedDuration),
                "indicatedBitrate": String(format: "%.0f", ev.indicatedBitrate),
                "observedBitrate": String(format: "%.0f", ev.observedBitrate),
                "transferDuration": String(format: "%.2f", ev.transferDuration),
            ])
            metrics["droppedFrames", default: 0] += Double(max(ev.numberOfDroppedVideoFrames, 0))
        }
        var errors: [[String: String]] = []
        for ev in item?.errorLog()?.events ?? [] {
            errors.append([
                "uri": redact(ev.uri),
                "status": String(ev.errorStatusCode),
                "domain": ev.errorDomain,
                "comment": redact(ev.errorComment),
            ])
            if ev.errorStatusCode >= 400 || ev.errorStatusCode < 0 {
                fail("errorLog: \(ev.errorStatusCode) \(ev.errorDomain) \(redact(ev.errorComment))")
            }
        }
        let result = failures.isEmpty ? "pass" : "fail"
        return Output(
            cell: e.cell, item: e.item, player: e.player, result: result,
            notes: failures + notes, metrics: metrics, accessLog: access, errorLog: errors,
            host: "macOS " + ProcessInfo.processInfo.operatingSystemVersionString,
            when: ISO8601DateFormatter().string(from: Date())
        )
    }

    func teardown() {
        player?.pause()
        player?.replaceCurrentItem(with: nil)
        for o in observers { NotificationCenter.default.removeObserver(o) }
        observers = []
    }
}

func round3(_ v: Double) -> Double { (v * 1000).rounded() / 1000 }

/// Stops the server's transcode for the play session, as the clients do
/// when playback ends.
func stopEncoding(_ e: PlanEntry, token: String) async {
    guard let u = URL(string: e.url), var c = URLComponents(url: u, resolvingAgainstBaseURL: false) else { return }
    c.path = "/Videos/ActiveEncodings"
    c.queryItems = [URLQueryItem(name: "deviceId", value: e.deviceId), URLQueryItem(name: "playSessionId", value: e.playSessionId)]
    guard let url = c.url else { return }
    var req = URLRequest(url: url)
    req.httpMethod = "DELETE"
    req.setValue("MediaBrowser Client=\"avprobe\", Device=\"avprobe\", DeviceId=\"\(e.deviceId)\", Version=\"1\", Token=\"\(token)\"", forHTTPHeaderField: "Authorization")
    _ = try? await URLSession.shared.data(for: req)
}

// MARK: - Main

func usage() -> Never {
    FileHandle.standardError.write("usage: avprobe --plan plan.json --out results.json [--cells ID,ID] [--seconds N]\n(the API key is read from stdin)\n".data(using: .utf8)!)
    exit(2)
}

@main
struct AVProbe {
    static func main() async {
        var planPath = "", outPath = "", cells = Set<String>()
        var seconds: Double?
        var args = CommandLine.arguments.dropFirst().makeIterator()
        while let a = args.next() {
            switch a {
            case "--plan": planPath = args.next() ?? ""
            case "--out": outPath = args.next() ?? ""
            case "--cells": cells = Set((args.next() ?? "").split(separator: ",").map(String.init))
            case "--seconds": seconds = Double(args.next() ?? "")
            default: usage()
            }
        }
        if planPath.isEmpty || outPath.isEmpty { usage() }
        let token = String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if token.isEmpty {
            FileHandle.standardError.write("avprobe: no API key on stdin\n".data(using: .utf8)!)
            exit(2)
        }
        guard let data = FileManager.default.contents(atPath: planPath),
              var plan = try? JSONDecoder().decode([PlanEntry].self, from: data)
        else {
            FileHandle.standardError.write("avprobe: cannot read plan \(planPath)\n".data(using: .utf8)!)
            exit(2)
        }
        if !cells.isEmpty { plan = plan.filter { cells.contains($0.cell) } }

        var results: [Output] = []
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys]
        for var e in plan {
            if let s = seconds { e.seconds = s }
            let probe = await MainActor.run { Probe(e, token: token) }
            let r = await probe.run()
            await stopEncoding(e, token: token)
            results.append(r)
            let line = "\(e.cell) \(e.item.prefix(8)) \(e.player) \(e.method): \(r.result) \(r.notes.prefix(3).joined(separator: "; "))\n"
            FileHandle.standardError.write(line.data(using: .utf8)!)
            if let out = try? enc.encode(results) {
                try? out.write(to: URL(fileURLWithPath: outPath))
            }
        }
    }
}
