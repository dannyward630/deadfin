# WP-P0-06 golden corpus regeneration

The golden corpora are generated only from the vendored `upstream/jellyfin` checkout at commit `b04614e18d2aeed8d3d3de920c381c6bd5b97bdb`. The recorder rejects any other checkout. It requires .NET SDK 10 because that is the pinned tree's target framework.

From the Jellyport root, regenerate the complete corpus:

```sh
tools/goldenrec/record.sh all --output-root deadfin
```

This starts by removing only `deadfin/internal/{playback,encoding}/testdata/golden`, then uses `dotnet run --configuration Release --project tools/goldenrec/GoldenRec.csproj` against the pinned projects. It copies the 19 `DeviceProfile` and 33 `MediaSourceInfo` fixtures verbatim, extracts the upstream `InlineData` assertions, records the StreamBuilder cross-product and the software-only EncodingHelper matrix, writes provenance, and validates all hashes.

To generate outside the nested checkout without modifying it:

```sh
tools/goldenrec/record.sh all --output-root "$(mktemp -d)/deadfin"
```

To validate committed assets without a .NET SDK:

```sh
tools/goldenrec/record.sh verify --output-root deadfin
```

`provenance.json` records the exact upstream pin, pinned input hashes, recorder-source hashes, command template, and generated asset hashes. `SHA256SUMS` is the corresponding stable manifest. Neither contains wall-clock metadata.

## Synthetic media fixtures

Generate the committed fixture projection only in its pinned linux/amd64
container:

```sh
tools/verify-env/generate-fixtures.sh
```

The image pins its base by digest and installs ffmpeg plus linked encoder
libraries from the Debian snapshot at `20260728T000000Z`. Compose enforces
`linux/amd64`, including on Darwin/arm64 Docker Desktop. Its entrypoint also
requires Linux/x86_64 and ffmpeg package `7:5.1.9-0+deb12u1`. The publisher
verifies the image attestation, Debian 12 snapshot sources, platform, and package
again. It then creates the candidate with a fixed ffmpeg path and a clean
environment. It verifies all 57 source entries and all 30 media entries before
it publishes under a repository lock. A rejected or incomplete candidate does
not change the accepted output.

On Apple Silicon, this will fail at the fixture-publisher step: the candidate
differs from `source-manifest.sha256` in exactly the 18 MPEG-4 `.mkv` entries,
and that is expected rather than a bug. Fixture correctness is judged on the
native x86_64 CI runner and only there.

The generator uses a fixed scalar single-threaded video profile for the 18
mpeg4 MKV files. Every mpeg4 MKV command passes `-cpuflags 0 -threads 1`
before its inputs. This disables the SIMD CPU dispatch and the encoder
thread pool. The `-cpuflags 0 -threads 1` flags do not make the 18 MPEG-4
files host-independent. The encoder still selects scalar code paths that
differ between host architectures, which is why the 18 MPEG-4 `.mkv` entries
diverge on Apple Silicon.

The HLG sample is a committed artifact, not an encoded file. The generator
copies `tools/goldenrec/fixtures/attested/hlg-sample.mkv`
into the fixture tree after it verifies the sha256 against the pinned digest
`HLG_ARTIFACT_SHA256` in `gen.sh`. A missing or corrupt artifact stops
generation before the target file is written. The generator copies the HLG
bytes verbatim and verifies the sha256, so the HLG entry is the same on every
host. macOS/arm64 generation is a verification
path, not an attestation authority. The native x86_64 CI runner remains the sole
attestation authority for `source-manifest.sha256`.

### HLG artifact provenance

The committed artifact is a genuine HEVC 10-bit BT.2020/ARIB STD-B67
stream. Its playback properties are: codec `hevc`, pixel format
`yuv420p10le`, resolution `320x180`, color primaries `bt2020`, transfer
`arib-std-b67`, color space `bt2020nc`, and no audio stream.

The artifact bytes were encoded in the canonical fixture-generator image
(base `golang:1.25.9-bookworm@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3`,
Debian snapshot `20260728T000000Z`, ffmpeg `7:5.1.9-0+deb12u1`,
`platform: linux/amd64`) running under QEMU emulation on a macOS/arm64
host. They were not encoded on the native CI runner. No CI job encodes them:
`gen.sh` copies the artifact instead of running libx265, so
`goldenrec-gate` never repeats this encode.

The native CI runner attests the digest, not the encode. The attested
`source-manifest.sha256` comes from `goldenrec-gate` on the native CI
runner, and its HLG entry is the sha256 of these exact bytes. The native
gate therefore accepts this byte stream. The encoding host does not affect
reproducibility, because every run copies these digest-pinned bytes instead
of re-encoding them. The encoding host only decided which byte stream the
repository froze.

The exact ffmpeg command at commit `99bc269`:

```sh
ffmpeg -hide_banner -loglevel error -y -cpuflags 0 -f lavfi -i 'testsrc2=size=320x180:rate=24' -t 1 \
  -vf format=yuv420p10le -c:v libx265 -x265-params 'hdr-opt=1:colorprim=bt2020:transfer=arib-std-b67:colormatrix=bt2020nc:asm=0:pools=none:frame-threads=1' \
  -color_primaries bt2020 -color_trc arib-std-b67 -colorspace bt2020nc -an -fflags +bitexact <output>
```

### HLG artifact regeneration

To regenerate the HLG artifact, run the recorded command in the canonical
image on a native x86_64 host. Then update three things together: the
artifact at `tools/goldenrec/fixtures/attested/hlg-sample.mkv`, the pinned
`HLG_ARTIFACT_SHA256` constant in `gen.sh`, and the HLG entry in
`source-manifest.sha256`. Re-attest the full manifest through the
`goldenrec-gate` workflow after the artifact update.

To make an intentional canonical toolchain change, update the pinned image
definition first. Then dispatch the `goldenrec-gate` workflow with
`attest=true`.

The `goldenrec-gate` workflow runs on the native x86_64 CI runner. With
`attest=true`, it runs `tools/verify-env/generate-fixtures.sh --attest`
inside the canonical container and uploads the validated
`source-manifest.sha256` as a downloadable artifact. It does not commit or
push the manifest. Direct host runs of `fixtures/gen.sh` cannot select the
committed output, even when a caller sets internal environment variables.
Direct publisher calls cannot submit an existing candidate. An isolated
`GOLDENREC_FIXTURES_DIR` remains available for generator tests.

## Compiled-assembly fallback

The normal source-project build is preferred and was used for the committed corpus. If a future pinned upstream tree cannot be built as projects, first build that exact upstream solution, set `GOLDENREC_UPSTREAM_ROOT` to that checked-out tree, and replace the project references in `GoldenRec.csproj` with the matching `MediaBrowser.Model.dll` / controller dependency closure from that solution build. Record the assembly SHA-256 values in the affected corpus provenance before accepting the output. Never substitute assemblies from a different commit.
