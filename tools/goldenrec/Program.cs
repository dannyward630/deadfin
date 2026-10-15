using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.RegularExpressions;
using Jellyfin.Data.Enums;
using Jellyfin.Extensions.Json;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Controller.IO;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.MediaEncoding;
using MediaBrowser.Controller.Streaming;
using MediaBrowser.Model.Configuration;
using MediaBrowser.Model.Dlna;
using MediaBrowser.Model.Dto;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.MediaInfo;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Moq;

internal static class Program
{
    private const string Pin = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb";
    private const string GoldenSubtitlePath = "/cache/goldenrec-subtitles.srt";

    private static readonly JsonSerializerOptions OutputJson = new(JsonSerializerOptions.Default)
    {
        WriteIndented = true,
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase
    };

    private static async Task<int> Main(string[] args)
    {
        if (args.Length == 0 || args[0] is "--help" or "-h")
        {
            Console.Error.WriteLine("Usage: GoldenRec {streambuilder|encoding|all} --root DEADFIN_ROOT --upstream JELLYFIN_ROOT");
            return args.Length == 0 ? 64 : 0;
        }

        var root = RequireOption(args, "--root");
        var upstream = RequireOption(args, "--upstream");
        ValidatePinnedTree(upstream);
        switch (args[0])
        {
            case "streambuilder":
                await RecordStreamBuilder(root, upstream);
                break;
            case "encoding":
                await RecordEncoding(root, upstream);
                break;
            case "all":
                await RecordStreamBuilder(root, upstream);
                await RecordEncoding(root, upstream);
                break;
            default:
                throw new ArgumentException("unknown recorder: " + args[0]);
        }

        return 0;
    }

    private static string RequireOption(string[] args, string option)
    {
        var index = Array.IndexOf(args, option);
        if (index < 0 || index + 1 == args.Length)
        {
            throw new ArgumentException("missing " + option);
        }

        return Path.GetFullPath(args[index + 1]);
    }

    private static void ValidatePinnedTree(string upstream)
    {
        var head = File.ReadAllText(Path.Combine(upstream, ".git", "HEAD")).Trim();
        if (head.StartsWith("ref: ", StringComparison.Ordinal))
        {
            var reference = head[5..];
            head = File.ReadAllText(Path.Combine(upstream, ".git", reference)).Trim();
        }

        if (!string.Equals(head, Pin, StringComparison.Ordinal))
        {
            throw new InvalidOperationException($"upstream checkout is {head}, expected pinned {Pin}");
        }
    }

    private static async Task RecordStreamBuilder(string root, string upstream)
    {
        var fixtureRoot = Path.Combine(upstream, "tests", "Jellyfin.Model.Tests", "Test Data");
        var playbackRoot = Path.Combine(root, "internal", "playback", "testdata");
        Directory.CreateDirectory(playbackRoot);
        var profiles = Directory.GetFiles(fixtureRoot, "DeviceProfile-*.json").OrderBy(Path.GetFileName, StringComparer.Ordinal).ToArray();
        var sources = Directory.GetFiles(fixtureRoot, "MediaSourceInfo-*.json").OrderBy(Path.GetFileName, StringComparer.Ordinal).ToArray();
        if (profiles.Length != 19 || sources.Length != 33)
        {
            throw new InvalidOperationException($"expected 19 profiles and 33 sources, got {profiles.Length} and {sources.Length}");
        }

        foreach (var input in profiles.Concat(sources))
        {
            var output = Path.Combine(playbackRoot, Path.GetFileName(input));
            File.Copy(input, output, overwrite: true);
        }

        var tests = Path.Combine(upstream, "tests", "Jellyfin.Model.Tests", "Dlna", "StreamBuilderTests.cs");
        var assertions = ExtractAssertions(await File.ReadAllTextAsync(tests));
        await WriteJson(Path.Combine(playbackRoot, "streambuilder-assertions.yaml"), new
        {
            format = "deadfin.streambuilder.assertions/v1",
            upstream = new { commit = Pin, source = "tests/Jellyfin.Model.Tests/Dlna/StreamBuilderTests.cs", sha256 = HashFile(tests) },
            cases = assertions
        });

        var builder = new StreamBuilder(new NoTranscoderSupport(), NullLogger.Instance);
        var cases = new List<object>(profiles.Length * sources.Length);
        foreach (var profileFile in profiles)
        {
            var profileName = FixtureName(profileFile, "DeviceProfile-");
            var profile = Deserialize<DeviceProfile>(profileFile);
            foreach (var sourceFile in sources)
            {
                var sourceName = FixtureName(sourceFile, "MediaSourceInfo-");
                var source = Deserialize<MediaSourceInfo>(sourceFile);
                var options = NewMediaOptions(profile, source);
                var video = builder.GetOptimalVideoStream(options);
                var audio = TryBuildAudio(builder, NewMediaOptions(profile, source));
                cases.Add(new
                {
                    id = profileName + "--" + sourceName,
                    profile = profileName,
                    source = sourceName,
                    video = StreamOutput(video),
                    audio
                });
            }
        }

        var golden = Path.Combine(playbackRoot, "golden");
        Directory.CreateDirectory(golden);
        await WriteJson(Path.Combine(golden, "streambuilder.json"), new
        {
            format = "deadfin.streambuilder.golden/v1",
            upstreamPin = Pin,
            cases
        });
    }

    private static object? StreamOutput(StreamInfo? stream)
    {
        if (stream is null)
        {
            return null;
        }

        return new
        {
            streamInfo = JsonNode.Parse(JsonSerializer.Serialize(stream, JsonDefaults.Options)),
            url = stream.ToUrl("media:", "ACCESSTOKEN", null)
        };
    }
    private static object? TryBuildAudio(StreamBuilder builder, MediaOptions options)
    {
        try
        {
            return StreamOutput(builder.GetOptimalAudioStream(options));
        }
        catch (ArgumentNullException exception)
        {
            // The upstream builder rejects source fixtures without audio. Preserve the
            // observed result instead of dropping that profile×source cell.
            return new { error = exception.GetType().Name, parameter = exception.ParamName };
        }
    }


    private static MediaOptions NewMediaOptions(DeviceProfile profile, MediaSourceInfo source) => new()
    {
        ItemId = new Guid("11D229B7-2D48-4B95-9F9B-49F6AB75E613"),
        MediaSourceId = source.Id,
        MediaSources = [source],
        DeviceId = "goldenrec-device",
        Profile = profile,
        AllowAudioStreamCopy = true,
        AllowVideoStreamCopy = true,
        EnableDirectStream = false
    };

    private static async Task RecordEncoding(string root, string upstream)
    {
        var fixtureRoot = Path.Combine(upstream, "tests", "Jellyfin.Model.Tests", "Test Data");
        var output = Path.Combine(root, "internal", "encoding", "testdata", "golden");
        Directory.CreateDirectory(output);
        var helper = CreateEncodingHelper();
        var options = new EncodingOptions { HardwareAccelerationType = HardwareAccelerationType.none, EnableTonemapping = true };
        var cases = new List<object>();
        var sources = new (string Name, string Codec)[]
        {
            ("mp4-h264-ac3-aac-srt-2600k", "h264"),
            ("mp4-hevc-ac3-aac-srt-15200k", "hevc"),
            ("mkv-av1-aac-srt-2600k", "av1")
        };
        var subtitles = new[] { "none", "text", "pgs" };
        var ranges = new[] { "sdr", "hdr10", "hlg", "dv" };
        var audioVariants = new[] { "copy", "aac", "downmix" };
        var containers = new[] { "ts", "fmp4" };
        var seeks = new long?[] { null, TimeSpan.FromSeconds(90).Ticks };

        foreach (var sourceSpec in sources)
        foreach (var subtitle in subtitles)
        foreach (var range in ranges)
        foreach (var audioVariant in audioVariants)
        foreach (var container in containers)
        foreach (var seek in seeks)
        {
            var sourceFile = Path.Combine(fixtureRoot, "MediaSourceInfo-" + sourceSpec.Name + ".json");
            var source = Deserialize<MediaSourceInfo>(sourceFile);
            var state = BuildEncodingState(source, sourceSpec.Name.StartsWith("mkv-", StringComparison.Ordinal) ? "mkv" : "mp4", subtitle, range, audioVariant, container, seek);
            var videoCodec = helper.GetVideoEncoder(state, options);
            var input = helper.GetInputArgument(state, options, container == "fmp4" ? "mp4" : "ts");
            var map = helper.GetMapArgs(state);
            var video = helper.GetProgressiveVideoArguments(state, options, videoCodec, EncoderPreset.auto);
            var audio = helper.GetProgressiveVideoAudioArguments(state, options);
            var filter = helper.GetVideoProcessingFilterParam(state, options, videoCodec);
            var progressive = IncludeInputArgument(helper.GetProgressiveVideoFullCommandLine(state, options, EncoderPreset.auto));
            var hls = IncludeInputArgument(BuildHlsCommandLine(helper, state, options, container == "fmp4" ? "mp4" : "ts"));
            var seekName = seek.HasValue ? "offset" : "start";
            var facts = BuildEncodingCaseFacts(helper, state, options, videoCodec);

            cases.Add(new
            {
                id = $"encoding/{sourceSpec.Codec}/{subtitle}/{range}/{audioVariant}/{container}/{seekName}",
                inputCodec = sourceSpec.Codec,
                fixture = sourceSpec.Name,
                subtitle,
                tonemap = range,
                audio = audioVariant,
                container,
                seek = seekName,
                seekOffsetTicks = seek ?? 0,
                subtitlePath = facts.SubtitlePath,
                subtitleInputIndex = facts.SubtitleInputIndex,
                subtitleStreamIndex = facts.SubtitleStreamIndex,
                subtitleCodec = facts.SubtitleCodec,
                audioCodec = facts.AudioCodec,
                audioStreamIndex = facts.AudioStreamIndex,
                audioChannels = facts.AudioChannels,
                downmixAudioBoost = facts.DownmixAudioBoost,
                nalLengthSize = facts.NalLengthSize,
                capabilities = facts.Capabilities,
                selectedVideoEncoder = facts.SelectedVideoEncoder,
                selectedAudioEncoder = facts.SelectedAudioEncoder,

                argv = new { input, map, video, audio, filter, progressive, hls }
            });
        }

        var progressiveRemuxCases = new List<object>();
        const string progressiveRemuxFixture = "mp4-h264-ac3-aac-srt-2600k";
        foreach (var container in containers)
        foreach (var seek in seeks)
        {
            var sourceFile = Path.Combine(fixtureRoot, "MediaSourceInfo-" + progressiveRemuxFixture + ".json");
            var source = Deserialize<MediaSourceInfo>(sourceFile);
            var state = BuildProgressiveRemuxState(source, "mp4", container, seek);
            var initialVideoCodec = state.OutputVideoCodec;
            var initialAudioCodec = state.OutputAudioCodec;
            helper.TryStreamCopy(state, options);
            var videoCodec = helper.GetVideoEncoder(state, options);
            var input = helper.GetInputArgument(state, options, container == "fmp4" ? "mp4" : "ts");
            var map = helper.GetMapArgs(state);
            var video = helper.GetProgressiveVideoArguments(state, options, videoCodec, EncoderPreset.auto);
            var audio = helper.GetProgressiveVideoAudioArguments(state, options);
            var progressive = IncludeInputArgument(helper.GetProgressiveVideoFullCommandLine(state, options, EncoderPreset.auto));
            var seekName = seek.HasValue ? "offset" : "start";
            var facts = BuildEncodingCaseFacts(helper, state, options, videoCodec);

            progressiveRemuxCases.Add(new
            {
                id = $"progressive-remux/h264/{container}/{seekName}",
                fixture = progressiveRemuxFixture,
                container,
                seek = seekName,
                seekOffsetTicks = seek ?? 0,
                initialVideoCodec,
                initialAudioCodec,
                selectedVideoCodec = state.OutputVideoCodec,
                selectedAudioCodec = state.OutputAudioCodec,
                subtitlePath = facts.SubtitlePath,
                subtitleInputIndex = facts.SubtitleInputIndex,
                subtitleStreamIndex = facts.SubtitleStreamIndex,
                subtitleCodec = facts.SubtitleCodec,
                audioCodec = facts.AudioCodec,
                audioStreamIndex = facts.AudioStreamIndex,
                audioChannels = facts.AudioChannels,
                downmixAudioBoost = facts.DownmixAudioBoost,
                nalLengthSize = facts.NalLengthSize,
                capabilities = facts.Capabilities,
                selectedVideoEncoder = facts.SelectedVideoEncoder,
                selectedAudioEncoder = facts.SelectedAudioEncoder,

                argv = new { input, map, video, audio, progressive }
            });
        }
        const string progressiveEdgeFixture = "mp4-h264-ac3-aac-srt-2600k";
        var progressiveEdgeSeek = TimeSpan.FromMilliseconds(1500).Ticks;
        var progressiveEdgeSource = Deserialize<MediaSourceInfo>(Path.Combine(fixtureRoot, "MediaSourceInfo-" + progressiveEdgeFixture + ".json"));
        var progressiveEdgeState = BuildEncodingState(progressiveEdgeSource, "mp4", "text", "sdr", "aac", "ts", progressiveEdgeSeek);
        var progressiveEdgeVideoCodec = helper.GetVideoEncoder(progressiveEdgeState, options);
        var progressiveEdgeFacts = BuildEncodingCaseFacts(helper, progressiveEdgeState, options, progressiveEdgeVideoCodec);
        var progressiveEdgeCases = new[]
        {
            new
            {
                id = "progressive-edge/text-subtitle/1.5s",
                fixture = progressiveEdgeFixture,
                subtitle = "text",
                audio = "aac",
                container = "ts",
                seekOffsetTicks = progressiveEdgeSeek,
                subtitlePath = progressiveEdgeFacts.SubtitlePath,
                subtitleInputIndex = progressiveEdgeFacts.SubtitleInputIndex,
                subtitleStreamIndex = progressiveEdgeFacts.SubtitleStreamIndex,
                subtitleCodec = progressiveEdgeFacts.SubtitleCodec,
                audioCodec = progressiveEdgeFacts.AudioCodec,
                audioStreamIndex = progressiveEdgeFacts.AudioStreamIndex,
                audioChannels = progressiveEdgeFacts.AudioChannels,
                downmixAudioBoost = progressiveEdgeFacts.DownmixAudioBoost,
                nalLengthSize = progressiveEdgeFacts.NalLengthSize,
                capabilities = progressiveEdgeFacts.Capabilities,
                selectedVideoEncoder = progressiveEdgeFacts.SelectedVideoEncoder,
                selectedAudioEncoder = progressiveEdgeFacts.SelectedAudioEncoder,
                argv = new
                {
                    progressive = IncludeInputArgument(helper.GetProgressiveVideoFullCommandLine(progressiveEdgeState, options, EncoderPreset.auto))
                }
            }
        };


        await WriteJson(Path.Combine(output, "encoding.json"), new
        {
            format = "deadfin.encoding.golden/v1",
            upstreamPin = Pin,

            hardwareAccelerationType = "none",
            dimensions = new
            {
                inputCodec = sources.Select(source => source.Codec).ToArray(),
                subtitleBurnIn = subtitles,
                toneMap = ranges,
                audio = audioVariants,
                container = containers,
                seekOffsetTicks = new[] { 0L, TimeSpan.FromSeconds(90).Ticks }
            },
            cases,
            progressiveRemuxCases,
            progressiveEdgeCases,


        });
    }

    private static string IncludeInputArgument(string command) => command.StartsWith("-i ", StringComparison.Ordinal) ? " " + command : command;
    private static EncodingCaseFacts BuildEncodingCaseFacts(EncodingHelper helper, StreamState state, EncodingOptions options, string videoEncoder)
    {
        var video = state.VideoStream ?? throw new InvalidOperationException("encoding state has no video stream");
        var audio = state.AudioStream ?? throw new InvalidOperationException("encoding state has no audio stream");
        var subtitle = state.SubtitleStream;
        var audioEncoder = helper.GetAudioEncoder(state);
        return new EncodingCaseFacts(
            subtitle?.IsTextSubtitleStream == true ? GoldenSubtitlePath : string.Empty,
            subtitle is null ? -1 : 0,
            subtitle?.Index ?? -1,
            subtitle?.Codec ?? string.Empty,
            audio.Codec ?? throw new InvalidOperationException("encoding state audio stream has no codec"),
            audio.Index,
            audio.Channels ?? 0,
            options.DownMixAudioBoost,
            video.NalLengthSize ?? string.Empty,
            new EncodingCapabilities("libx264", ["aac_at", "libfdk_aac", "aac"], true),
            string.Equals(videoEncoder, "copy", StringComparison.OrdinalIgnoreCase) ? null : videoEncoder,
            string.Equals(audioEncoder, "copy", StringComparison.OrdinalIgnoreCase) ? null : audioEncoder);
    }
    private sealed record EncodingCaseFacts(
        string SubtitlePath,
        int SubtitleInputIndex,
        int SubtitleStreamIndex,
        string SubtitleCodec,
        string AudioCodec,
        int AudioStreamIndex,
        int AudioChannels,
        double DownmixAudioBoost,
        string NalLengthSize,
        EncodingCapabilities Capabilities,
        string? SelectedVideoEncoder,
        string? SelectedAudioEncoder);

    private sealed record EncodingCapabilities(string H264Encoder, string[] AACEncoders, bool SupportsTonemapx);



    private static StreamState BuildEncodingState(MediaSourceInfo source, string fixtureExtension, string subtitleVariant, string range, string audioVariant, string container, long? seek)
    {
        var video = source.MediaStreams.First(stream => stream.Type == MediaStreamType.Video);
        var audio = source.MediaStreams.First(stream => stream.Type == MediaStreamType.Audio);
        switch (range)
        {
            case "sdr":
                video.ColorTransfer = null;
                video.CodecTag = null;
                video.DvProfile = null;
                break;
            case "hdr10":
                video.ColorTransfer = "smpte2084";
                break;
            case "hlg":
                video.ColorTransfer = "arib-std-b67";
                break;

            case "dv":
                video.CodecTag = "dvhe";
                video.DvProfile = 5;
                video.RpuPresentFlag = 1;
                video.BlPresentFlag = 1;
                video.DvBlSignalCompatibilityId = 0;
                break;
            default:
                throw new ArgumentOutOfRangeException(nameof(range));
        }

        video.BitDepth = range == "sdr" ? 8 : 10;

        MediaStream? subtitle = null;
        if (subtitleVariant != "none")
        {
            subtitle = source.MediaStreams.First(stream => stream.Type == MediaStreamType.Subtitle);
            subtitle.Codec = subtitleVariant == "text" ? "srt" : "pgssub";
            subtitle.IsExternal = false;
            subtitle.SupportsExternalStream = subtitleVariant == "text";
            if (subtitleVariant == "pgs")
            {
                subtitle.Path = null;
            }
        }

        var request = new VideoRequestDto
        {
            VideoCodec = "h264",
            AudioCodec = audioVariant == "copy" ? "copy" : "aac",
            SegmentContainer = container == "fmp4" ? "mp4" : "ts",
            StartTimeTicks = seek,
            Width = 1280,
            Height = 720,
            EnableAutoStreamCopy = true,
            AllowAudioStreamCopy = audioVariant == "copy",
            AllowVideoStreamCopy = false
        };
        return new StreamState(
            Mock.Of<IMediaSourceManager>(),
            TranscodingJobType.Progressive,
            Mock.Of<ITranscodeManager>())
        {
            MediaSource = source,
            MediaPath = "/media/goldenrec-input." + fixtureExtension,
            InputProtocol = MediaProtocol.File,
            InputContainer = source.Container,
            VideoStream = video,
            AudioStream = audio,
            SubtitleStream = subtitle,
            SubtitleDeliveryMethod = subtitleVariant == "none" ? SubtitleDeliveryMethod.Drop : SubtitleDeliveryMethod.Encode,
            OutputVideoCodec = "h264",
            OutputAudioCodec = audioVariant == "copy" ? "copy" : "aac",
            OutputAudioChannels = audioVariant == "downmix" ? 2 : null,
            OutputAudioBitrate = audioVariant == "copy" ? null : 192000,
            OutputVideoBitrate = 2_500_000,
            OutputContainer = container == "fmp4" ? "mp4" : "ts",
            OutputFilePath = "/cache/goldenrec-progressive." + (container == "fmp4" ? "mp4" : "ts"),
            SupportedAudioCodecs = ["aac", "ac3", "eac3"],
            SupportedVideoCodecs = ["h264"],
            SupportedSubtitleCodecs = ["srt"],
            Request = request,
            IsInputVideo = true,
            RunTimeTicks = TimeSpan.FromMinutes(10).Ticks
        };
    }

    private static StreamState BuildProgressiveRemuxState(MediaSourceInfo source, string fixtureExtension, string container, long? seek)
    {
        var video = source.MediaStreams.First(stream => stream.Type == MediaStreamType.Video);
        var audio = source.MediaStreams.First(stream => stream.Type == MediaStreamType.Audio);
        var videoCodec = video.Codec ?? throw new InvalidOperationException("remux fixture has no video codec");
        var audioCodec = audio.Codec ?? throw new InvalidOperationException("remux fixture has no audio codec");
        var request = new VideoRequestDto
        {
            VideoCodec = videoCodec,
            AudioCodec = audioCodec,
            SegmentContainer = container == "fmp4" ? "mp4" : "ts",
            StartTimeTicks = seek,
            EnableAutoStreamCopy = true,
            AllowAudioStreamCopy = true,
            AllowVideoStreamCopy = true
        };
        return new StreamState(
            Mock.Of<IMediaSourceManager>(),
            TranscodingJobType.Progressive,
            Mock.Of<ITranscodeManager>())
        {
            MediaSource = source,
            MediaPath = "/media/goldenrec-input." + fixtureExtension,
            InputProtocol = MediaProtocol.File,
            InputContainer = source.Container,
            VideoStream = video,
            AudioStream = audio,
            SubtitleDeliveryMethod = SubtitleDeliveryMethod.Drop,
            OutputVideoCodec = videoCodec,
            OutputAudioCodec = audioCodec,
            OutputContainer = container == "fmp4" ? "mp4" : "ts",
            OutputFilePath = "/cache/goldenrec-progressive." + (container == "fmp4" ? "mp4" : "ts"),
            SupportedAudioCodecs = [audioCodec],
            SupportedVideoCodecs = [videoCodec],
            Request = request,
            IsInputVideo = true,
            RunTimeTicks = TimeSpan.FromMinutes(10).Ticks
        };
    }

    private static EncodingHelper CreateEncodingHelper()
    {
        var mediaEncoder = new Mock<IMediaEncoder>(MockBehavior.Loose);
        mediaEncoder.SetupGet(value => value.EncoderVersion).Returns(new Version(7, 0));
        mediaEncoder.Setup(value => value.GetInputPathArgument(It.IsAny<EncodingJobInfo>())).Returns((EncodingJobInfo state) => $"file:\"{state.MediaPath}\"");
        mediaEncoder.Setup(value => value.GetTimeParameter(It.IsAny<long>())).Returns((long ticks) => TimeSpan.FromTicks(ticks).ToString(@"hh\:mm\:ss\.fff", System.Globalization.CultureInfo.InvariantCulture));
        mediaEncoder.Setup(value => value.EscapeSubtitleFilterPath(It.IsAny<string>())).Returns((string path) => path);
        mediaEncoder.Setup(value => value.SupportsEncoder(It.IsAny<string>())).Returns(true);
        mediaEncoder.Setup(value => value.SupportsDecoder(It.IsAny<string>())).Returns(true);
        mediaEncoder.Setup(value => value.SupportsFilter(It.IsAny<string>())).Returns(true);
        mediaEncoder.Setup(value => value.SupportsHwaccel(It.IsAny<string>())).Returns(false);
        mediaEncoder.Setup(value => value.CanEncodeToAudioCodec(It.IsAny<string>())).Returns(true);
        mediaEncoder.Setup(value => value.CanEncodeToSubtitleCodec(It.IsAny<string>())).Returns(true);
        mediaEncoder.Setup(value => value.CanExtractSubtitles(It.IsAny<string>())).Returns(false);
        var subtitleEncoder = new Mock<ISubtitleEncoder>(MockBehavior.Loose);
        subtitleEncoder.Setup(value => value.GetSubtitleFilePath(It.IsAny<MediaStream>(), It.IsAny<MediaSourceInfo>(), It.IsAny<CancellationToken>())).Returns(Task.FromResult("/cache/goldenrec-subtitles.srt"));
        return new EncodingHelper(
            Mock.Of<IApplicationPaths>(),
            mediaEncoder.Object,
            subtitleEncoder.Object,
            Mock.Of<IConfiguration>(),
            Mock.Of<MediaBrowser.Common.Configuration.IConfigurationManager>(),
            Mock.Of<IPathManager>());
    }

    // Mirrors DynamicHlsController.GetCommandLineArguments and its private audio/video
    // argument builders at b04614e. The controller methods are private, so this is a
    // source-pinned transcription rather than a reflection-only test seam.
    private static string BuildHlsCommandLine(EncodingHelper helper, StreamState state, EncodingOptions options, string segmentContainer)
    {
        var videoCodec = helper.GetVideoEncoder(state, options);
        var threads = EncodingHelper.GetNumberOfThreads(state, options, videoCodec);
        var outputPath = "/cache/goldenrec/master.m3u8";
        var outputPrefix = "/cache/goldenrec/master";
        var extension = segmentContainer == "mp4" ? ".mp4" : ".ts";
        var segmentFormat = segmentContainer == "mp4"
            ? "fmp4 -hls_fmp4_init_filename \"master-1.mp4\" -hls_segment_options movflags=+frag_discont"
            : "mpegts";
        var hlsArguments = "-hls_playlist_type vod -hls_list_size 0";
        return string.Format(
            System.Globalization.CultureInfo.InvariantCulture,
            "{0} {1} -map_metadata -1 -map_chapters -1 -threads {2} {3} {4} {5} -copyts -avoid_negative_ts disabled -max_muxing_queue_size 2048 -f hls -max_delay 5000000 -hls_time {6} -hls_segment_type {7} -start_number 0 -hls_segment_filename \"{8}\" {9} -y \"{10}\"",
            helper.GetInputModifier(state, options, segmentContainer),
            helper.GetInputArgument(state, options, segmentContainer),
            threads,
            state.IsOutputVideo ? helper.GetMapArgs(state) : string.Empty,
            GetHlsVideoArguments(helper, state, options, segmentContainer),
            GetHlsAudioArguments(helper, state, options),
            state.SegmentLength,
            segmentFormat,
            outputPrefix + "%d" + extension,
            hlsArguments,
            outputPath).Trim();
    }

    private static string GetHlsVideoArguments(EncodingHelper helper, StreamState state, EncodingOptions options, string segmentContainer)
    {
        if (state.VideoStream is null || !state.IsOutputVideo)
        {
            return string.Empty;
        }

        var codec = helper.GetVideoEncoder(state, options);
        var args = "-codec:v:0 " + codec;
        if (EncodingHelper.IsCopyCodec(codec))
        {
            if (!string.Equals(state.VideoStream.NalLengthSize, "0", StringComparison.OrdinalIgnoreCase))
            {
                var bitStreamArgs = helper.GetBitStreamArgs(state, MediaStreamType.Video);
                if (!string.IsNullOrEmpty(bitStreamArgs)) args += " " + bitStreamArgs;
            }

            args += " -start_at_zero";
        }
        else
        {
            args += helper.GetVideoQualityParam(state, codec, options, EncoderPreset.auto);
            args += helper.GetHlsVideoKeyFrameArguments(state, codec, state.SegmentLength, false, 0);
            var filter = helper.GetVideoProcessingFilterParam(state, options, codec);
            args = helper.GetNegativeMapArgsByFilters(state, filter) + args + filter;
            if (state.SubtitleStream is not null && !(state.SubtitleStream.IsExternal && !state.SubtitleStream.IsTextSubtitleStream)) args += " -start_at_zero";
        }

        if (!string.IsNullOrEmpty(state.OutputVideoSync)) args += EncodingHelper.GetVideoSyncOption(state.OutputVideoSync, new Version(7, 0));
        return args + helper.GetOutputFFlags(state);
    }

    private static string GetHlsAudioArguments(EncodingHelper helper, StreamState state, EncodingOptions options)
    {
        if (state.AudioStream is null) return string.Empty;
        var codec = helper.GetAudioEncoder(state);
        var bitStreamArgs = helper.GetAudioBitStreamArguments(state, state.Request.SegmentContainer, state.MediaSource.Container);
        if (EncodingHelper.IsCopyCodec(codec)) return "-codec:a:0 copy" + bitStreamArgs;
        var args = "-codec:a:0 " + codec + bitStreamArgs;
        var channels = state.OutputAudioChannels;
        if (channels.HasValue && (channels.Value != 2 || state.AudioStream.Channels is not null)) args += " -ac " + channels.Value;
        if (state.OutputAudioBitrate.HasValue) args += " -ab " + state.OutputAudioBitrate.Value.ToString(System.Globalization.CultureInfo.InvariantCulture);
        if (state.OutputAudioSampleRate.HasValue) args += " -ar " + state.OutputAudioSampleRate.Value.ToString(System.Globalization.CultureInfo.InvariantCulture);
        return args + helper.GetAudioFilterParam(state, options);
    }

    private static List<object> ExtractAssertions(string source)
    {
        var cases = new List<object>();
        var blocks = Regex.Matches(source, @"\[Theory\](?<rows>.*?)public async Task (?<method>BuildVideoItem\w+)\(", RegexOptions.Singleline);
        foreach (System.Text.RegularExpressions.Match block in blocks)
        {
            var method = block.Groups["method"].Value;
            var index = 0;
            foreach (System.Text.RegularExpressions.Match row in Regex.Matches(block.Groups["rows"].Value, @"\[InlineData\((?<args>.*)\)\]"))
            {
                var args = SplitArguments(row.Groups["args"].Value);
                if (args.Count < 3 || !args[0].StartsWith('"') || !args[1].StartsWith('"'))
                {
                    continue;
                }

                var reasons = args.Count > 3 ? ParseReasons(args[3]) : [];
                cases.Add(new
                {
                    id = method + "-" + (++index).ToString("D3", System.Globalization.CultureInfo.InvariantCulture),
                    method,
                    profile = Unquote(args[0]),
                    source = Unquote(args[1]),
                    options = OptionsForMethod(method),
                    expect = new
                    {
                        playMethod = args[2] == "null" ? null : args[2].Replace("PlayMethod.", string.Empty, StringComparison.Ordinal),
                        transcodeReasons = reasons,
                        kind = args.Count > 4 ? UnquoteOrNull(args[4]) : null,
                        protocol = args.Count > 5 ? UnquoteOrNull(args[5]) : null
                    }
                });
            }
        }

        if (cases.Count < 150)
        {
            throw new InvalidOperationException("expected roughly 200 upstream StreamBuilder assertions; parsed " + cases.Count);
        }

        return cases;
    }

    private static object OptionsForMethod(string method) => method switch
    {
        "BuildVideoItemSimple" => new { audioStreamIndex = (int?)null, subtitleStreamIndex = (int?)null, enableDirectStream = false, allowAudioStreamCopy = true, allowVideoStreamCopy = true },
        "BuildVideoItemWithFirstExplicitStream" => new { audioStreamIndex = "1", subtitleStreamIndex = "last", enableDirectStream = false, allowAudioStreamCopy = true, allowVideoStreamCopy = true },
        "BuildVideoItemWithDirectPlayExplicitStreams" => new { audioStreamIndex = "penultimate", subtitleStreamIndex = "last", enableDirectStream = false, allowAudioStreamCopy = true, allowVideoStreamCopy = true },
        _ => throw new ArgumentOutOfRangeException(nameof(method))
    };

    private static List<string> SplitArguments(string input)
    {
        var values = new List<string>();
        var current = new StringBuilder();
        var depth = 0;
        var quoted = false;
        foreach (var character in input)
        {
            if (character == '"') quoted = !quoted;
            if (!quoted && character == '(') depth++;
            if (!quoted && character == ')') depth--;
            if (!quoted && depth == 0 && character == ',')
            {
                values.Add(current.ToString().Trim());
                current.Clear();
            }
            else current.Append(character);
        }

        values.Add(current.ToString().Trim());
        return values;
    }

    private static List<string> ParseReasons(string input) => input
        .Replace("(TranscodeReason)0", string.Empty, StringComparison.Ordinal)
        .Split('|', StringSplitOptions.TrimEntries | StringSplitOptions.RemoveEmptyEntries)
        .Select(value => value.Replace("TranscodeReason.", string.Empty, StringComparison.Ordinal))
        .ToList();

    private static string? UnquoteOrNull(string value) => value == "null" ? null : Unquote(value);
    private static string Unquote(string value) => value.Trim().Trim('"');
    private static string FixtureName(string path, string prefix) => Path.GetFileNameWithoutExtension(path)[prefix.Length..];
    private static T Deserialize<T>(string path) => JsonSerializer.Deserialize<T>(File.ReadAllText(path), JsonDefaults.Options) ?? throw new InvalidOperationException("invalid fixture " + path);
    private static async Task WriteJson(string path, object value) => await File.WriteAllTextAsync(path, JsonSerializer.Serialize(value, OutputJson) + "\n");
    private static string HashFile(string path) => Convert.ToHexStringLower(SHA256.HashData(File.ReadAllBytes(path)));

    private sealed class NoTranscoderSupport : ITranscoderSupport
    {
        public bool CanEncodeToAudioCodec(string codec) => false;
        public bool CanEncodeToSubtitleCodec(string codec) => false;
        public bool CanExtractSubtitles(string codec) => false;
    }
}
