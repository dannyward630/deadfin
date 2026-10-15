using System.Security.Cryptography;
using System.Diagnostics;
using System.IO.Compression;
using System.Reflection;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Serialization;
using Jellyfin.Extensions;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Model.Configuration;

const string ExpectedCommit = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb";
const string DiacriticsVersion = "4.1.8";
const string Icu4NVersion = "60.1.0-alpha.356";
const string Icu4NTransliteratorVersion = "60.1.0-alpha.356";
const string DiacriticsPackageSha256 = "2f2b17644a1e7b526edc71a2bdf5784ef7456334d8f9fd1cbbf214fc5a4e9f37";
const string Icu4NPackageSha256 = "d50c8e80eee934c7a812006d97aa3c206e28f75c03da11548112348ccd25b716";
const string Icu4NTransliteratorPackageSha256 = "44b37040d56a373f0e99f6ff982febcd0595abc73bb829cd746daa8b8c4999db";
// These are fixed bytes from a clean build of the pinned source graph. Every
// behavior entry point validates them before executing behavior or writing output.
const string JellyfinExtensionsAssemblySha256 = "ad4c52bc6392e5150545057c1e8f2c7768c0071f7d50f4438ff7752cf65aaa6e";
const string MediaBrowserControllerAssemblySha256 = "345da6b4996383bc2c25bbc45ae42d82da1106d08a94c00605573ad4f9cac993";
const string MediaBrowserModelAssemblySha256 = "d464af24f5c5929270e7f8716e9cc65ef6031c9ccb3b5a3d1ac07d0cab52e29e";
const string SystemPrivateCoreLibAssemblySha256 = "8ff1aaed362a39cb175c07d32b7ebf43574eeef5c795b733ac4a019bbb22a91a";
const string SystemTextRegularExpressionsAssemblySha256 = "f5b8faf6b9b9d5ee8d95d7591539c99d63cd326822270d9136caad0c542ad616";
const string SystemTextJsonAssemblySha256 = "9fbe44abb2c9c28f8a23b88899aba85f3e016ce94561faf2ec2b3f96ca97f109";
const string SystemTextEncodingsWebAssemblySha256 = "646e6d8e9aa910001bc0e88b777db5b10eca52542a380177da56bfa97450b69b";
const string SystemIOCompressionAssemblySha256 = "7d5527c2729c63054d1a80151f2b8ae79662ecd9a28955af19dfa1141a1f3e4d";
const string SystemSecurityCryptographyAssemblySha256 = "fe091f043a85e1b468b64961297c1f8088ce60d2c402e6cbeccd8670049ff6a9";

var arguments = ParseArguments(args);
if (arguments.Mode == OracleMode.GenerateDiacriticsGo)
{
    GenerateDiacriticsGo(arguments.Output);
    return;
}

if (arguments.Mode == OracleMode.GenerateUnicodeGo)
{
    GenerateUnicodeGo(arguments.Output);
    return;
}

var vectorsPath = Path.GetFullPath(arguments.Vectors!);
var outputPath = Path.GetFullPath(arguments.Output);
var repositoryRoot = FindRepositoryRoot(vectorsPath);
var upstreamRoot = Environment.GetEnvironmentVariable("ITEMNORMORACLE_UPSTREAM_ROOT") is { Length: > 0 } configuredUpstream
    ? Path.GetFullPath(configuredUpstream)
    : Path.Combine(repositoryRoot, "upstream", "jellyfin");

ValidateAuthoritativeUpstream(upstreamRoot);
ValidateTrustedBehaviorAssemblies();
var vectors = DeserializeVectors(vectorsPath);
var sourceHashes = BuildSourceHashes(repositoryRoot, upstreamRoot, vectorsPath);
var provenance = new Provenance(
    ExpectedCommit,
    ReadGitHead(upstreamRoot),
    DiacriticsVersion,
    Icu4NTransliteratorVersion,
    sourceHashes);
var cases = vectors.Cases!.Select(Execute).ToArray();
var unsigned = new OracleDocument("jellyfin-item-normalization-oracle/v1", provenance, cases, string.Empty);
var sha256 = Sha256Hex(Serialize(unsigned with { Sha256 = null }, trailingNewline: false));
var document = unsigned with { Sha256 = sha256 };
var rendered = Serialize(document, trailingNewline: true);

Directory.CreateDirectory(Path.GetDirectoryName(outputPath) ?? throw new InvalidOperationException("output has no parent directory"));
File.WriteAllText(outputPath, rendered, new UTF8Encoding(encoderShouldEmitUTF8Identifier: false));

static Arguments ParseArguments(string[] args)
{
    if (args.Length == 3 && args[0] == "--generate-diacritics-go" && args[1] == "--output")
    {
        return new Arguments(OracleMode.GenerateDiacriticsGo, null, args[2]);
    }

    if (args.Length == 3 && args[0] == "--generate-unicode-go" && args[1] == "--output")
    {
        return new Arguments(OracleMode.GenerateUnicodeGo, null, args[2]);
    }

    if (args.Length == 4 && args[0] == "--vectors" && args[2] == "--output")
    {
        return new Arguments(OracleMode.GenerateOracle, args[1], args[3]);
    }

    throw new ArgumentException("usage: itemnormoracle --vectors <vectors.json> --output <normalization-oracle.json> | itemnormoracle --generate-diacritics-go --output <diacritics_gen.go> | itemnormoracle --generate-unicode-go --output <unicode16_gen.go>");
}

static void GenerateDiacriticsGo(string configuredOutputPath)
{
    var outputPath = Path.GetFullPath(configuredOutputPath);
    var repositoryRoot = FindRepositoryRoot(outputPath);
    var upstreamRoot = Environment.GetEnvironmentVariable("ITEMNORMORACLE_UPSTREAM_ROOT") is { Length: > 0 } configuredUpstream
        ? Path.GetFullPath(configuredUpstream)
        : Path.Combine(repositoryRoot, "upstream", "jellyfin");
    ValidateAuthoritativeUpstream(upstreamRoot);
    ValidateTrustedBehaviorAssemblies();
    var diacriticsPackage = GetPinnedPackages().Single(package => package.PackageId == "diacritics" && package.Version == DiacriticsVersion);
    var (packageHash, _) = ValidatePackageAndLoadedAssembly(diacriticsPackage);


    var mappings = new List<DiacriticMapping>();
    for (var scalar = 0; scalar <= char.MaxValue; scalar++)
    {
        if (char.IsSurrogate((char)scalar))
        {
            continue;
        }

        AddDiacriticMapping(mappings, scalar, new string((char)scalar, 1));
    }

    var bmpCount = mappings.Count;
    for (var scalar = 0x10000; scalar <= 0x10ffff; scalar++)
    {
        AddDiacriticMapping(mappings, scalar, char.ConvertFromUtf32(scalar));
    }

    var supplementaryCount = mappings.Count - bmpCount;
    var mappingHash = Sha256Hex(string.Concat(mappings.Select(mapping => $"U+{mapping.Scalar:X6}={mapping.Replacement}\n")));
    var generatorHash = Sha256Bytes(File.ReadAllBytes(Path.Combine(repositoryRoot, "tools", "compat", "itemnormoracle", "Program.cs")));
    var projectHash = Sha256Bytes(File.ReadAllBytes(Path.Combine(repositoryRoot, "tools", "compat", "itemnormoracle", "ItemNormOracle.csproj")));
    var oracleAssemblyHash = ExecutingOracleAssemblyHash();
    var rendered = RenderDiacriticsGo(
        mappings,
        bmpCount,
        supplementaryCount,
        mappingHash,
        packageHash,
        generatorHash,
        projectHash,
        oracleAssemblyHash).ReplaceLineEndings("\n");

    Directory.CreateDirectory(Path.GetDirectoryName(outputPath) ?? throw new InvalidOperationException("output has no parent directory"));
    File.WriteAllText(outputPath, rendered, new UTF8Encoding(encoderShouldEmitUTF8Identifier: false));
    Console.Error.WriteLine($"generated {mappings.Count} changed mappings (BMP {bmpCount}, supplementary {supplementaryCount}); mapping SHA-256 {mappingHash}");
}

static void GenerateUnicodeGo(string configuredOutputPath)
{
    var outputPath = Path.GetFullPath(configuredOutputPath);
    var repositoryRoot = FindRepositoryRoot(outputPath);
    var upstreamRoot = Environment.GetEnvironmentVariable("ITEMNORMORACLE_UPSTREAM_ROOT") is { Length: > 0 } configuredUpstream
        ? Path.GetFullPath(configuredUpstream)
        : Path.Combine(repositoryRoot, "upstream", "jellyfin");
    ValidateAuthoritativeUpstream(upstreamRoot);
    ValidateTrustedBehaviorAssemblies();

    var lowercase = new List<ScalarMapping>();
    var uppercase = new List<ScalarMapping>();
    for (var scalar = 0; scalar <= 0x10ffff; scalar++)
    {
        if (scalar is >= 0xd800 and <= 0xdfff)
        {
            continue;
        }

        var value = new Rune(scalar);
        var lowered = Rune.ToLowerInvariant(value);
        if (lowered != value)
        {
            lowercase.Add(new ScalarMapping(scalar, lowered.Value));
        }

        var uppered = Rune.ToUpperInvariant(value);
        if (uppered != value)
        {
            uppercase.Add(new ScalarMapping(scalar, uppered.Value));
        }
    }

    var ordinalIgnoreCase = BuildOrdinalIgnoreCaseMappings(out var ordinalIgnoreCaseGroups);
    var ordinalIgnoreCaseHash = Sha256Hex(string.Concat(ordinalIgnoreCase.Select(mapping => $"O+{mapping.Scalar:X6}={mapping.Mapping:X6}\n")));

    var letterOrNumber = new List<int>();
    var whitespace = new List<int>();
    for (var scalar = 0; scalar <= char.MaxValue; scalar++)
    {
        var codeUnit = (char)scalar;
        if (char.IsLetter(codeUnit) || char.IsNumber(codeUnit))
        {
            letterOrNumber.Add(scalar);
        }

        if (char.IsWhiteSpace(codeUnit))
        {
            whitespace.Add(scalar);
        }
    }

    var letterOrNumberRanges = BuildRanges(letterOrNumber);
    var whitespaceRanges = BuildRanges(whitespace);
    var canonical = string.Concat(
        lowercase.Select(mapping => $"L+{mapping.Scalar:X6}={mapping.Mapping:X6}\n"),
        uppercase.Select(mapping => $"U+{mapping.Scalar:X6}={mapping.Mapping:X6}\n"),
        ordinalIgnoreCase.Select(mapping => $"O+{mapping.Scalar:X6}={mapping.Mapping:X6}\n"),
        letterOrNumber.Select(codeUnit => $"N+{codeUnit:X4}\n"),
        whitespace.Select(codeUnit => $"W+{codeUnit:X4}\n"));
    var tableHash = Sha256Hex(canonical);
    var generatorHash = Sha256Bytes(File.ReadAllBytes(Path.Combine(repositoryRoot, "tools", "compat", "itemnormoracle", "Program.cs")));
    var projectHash = Sha256Bytes(File.ReadAllBytes(Path.Combine(repositoryRoot, "tools", "compat", "itemnormoracle", "ItemNormOracle.csproj")));
    var oracleAssemblyHash = ExecutingOracleAssemblyHash();
    var coreLibAssemblyHash = ValidateTrustedLoadedAssembly("system-private-corelib", "System.Private.CoreLib", SystemPrivateCoreLibAssemblySha256, typeof(object).Assembly);
    var rendered = RenderUnicodeGo(lowercase, uppercase, ordinalIgnoreCase, ordinalIgnoreCaseGroups, ordinalIgnoreCaseHash, letterOrNumberRanges, whitespaceRanges, tableHash, generatorHash, projectHash, oracleAssemblyHash, coreLibAssemblyHash).ReplaceLineEndings("\n");

    Directory.CreateDirectory(Path.GetDirectoryName(outputPath) ?? throw new InvalidOperationException("output has no parent directory"));
    File.WriteAllText(outputPath, rendered, new UTF8Encoding(encoderShouldEmitUTF8Identifier: false));
    Console.Error.WriteLine($"generated Unicode scalar tables: lowercase {lowercase.Count}, uppercase {uppercase.Count}, OrdinalIgnoreCase {ordinalIgnoreCase.Count} changed mappings in {ordinalIgnoreCaseGroups} groups; UTF-16 BMP letter/number {letterOrNumber.Count} in {letterOrNumberRanges.Count} ranges, whitespace {whitespace.Count} in {whitespaceRanges.Count} ranges; table SHA-256 {tableHash}");
}

static List<CodeUnitRange> BuildRanges(IReadOnlyList<int> codeUnits)
{
    var ranges = new List<CodeUnitRange>();
    foreach (var codeUnit in codeUnits)
    {
        if (ranges.Count > 0 && codeUnit == ranges[^1].Last + 1)
        {
            ranges[^1] = ranges[^1] with { Last = codeUnit };
        }
        else
        {
            ranges.Add(new CodeUnitRange(codeUnit, codeUnit));
        }
    }

    return ranges;
}

static string RenderUnicodeGo(
    IReadOnlyList<ScalarMapping> lowercase,
    IReadOnlyList<ScalarMapping> uppercase,
    IReadOnlyList<ScalarMapping> ordinalIgnoreCase,
    int ordinalIgnoreCaseGroups,
    string ordinalIgnoreCaseHash,
    IReadOnlyList<CodeUnitRange> letterOrNumberRanges,
    IReadOnlyList<CodeUnitRange> whitespaceRanges,
    string tableHash,
    string generatorHash,
    string projectHash,
    string oracleAssemblyHash,
    string coreLibAssemblyHash)
{
    var output = new StringBuilder();
    output.AppendLine("// Code generated by tools/compat/itemnormoracle --generate-unicode-go; DO NOT EDIT.");
    output.AppendLine("//");
    output.AppendLine("// Source: .NET 10 StringComparer.OrdinalIgnoreCase grouping over every valid Unicode scalar; System.Text.Rune.ToLowerInvariant and Rune.ToUpperInvariant over every valid Unicode scalar; char.IsLetter || char.IsNumber and char.IsWhiteSpace over every BMP UTF-16 code unit.");
    output.AppendLine($"// Generator: tools/compat/itemnormoracle/Program.cs SHA-256: {generatorHash}.");
    output.AppendLine($"// Generator project: tools/compat/itemnormoracle/ItemNormOracle.csproj SHA-256: {projectHash}.");
    output.AppendLine($"// Generator assembly: ItemNormOracle.dll SHA-256: {oracleAssemblyHash}.");
    output.AppendLine($"// System.Private.CoreLib assembly SHA-256: {coreLibAssemblyHash}.");
    output.AppendLine($"// Scalar lowercase mappings: {lowercase.Count}; scalar uppercase mappings: {uppercase.Count}; OrdinalIgnoreCase scalar groups: {ordinalIgnoreCaseGroups}; changed scalar mappings: {ordinalIgnoreCase.Count}; UTF-16 Letter/Number ranges: {letterOrNumberRanges.Count}; UTF-16 whitespace ranges: {whitespaceRanges.Count}.");
    output.AppendLine($"// OrdinalIgnoreCase canonical mapping SHA-256: {ordinalIgnoreCaseHash}.");
    output.AppendLine($"// Canonical table SHA-256: {tableHash}.");
    output.AppendLine("// Surrogate code points are excluded from scalar mappings; callers enforce the UTF-16 category seam.");
    output.AppendLine();
    output.AppendLine("package item");
    output.AppendLine();
    RenderScalarMapping(output, "invariantLowerScalar", lowercase);
    output.AppendLine();
    RenderScalarMapping(output, "invariantUpperScalar", uppercase);
    output.AppendLine();
    RenderScalarMapping(output, "ordinalIgnoreCaseScalar", ordinalIgnoreCase);
    output.AppendLine();
    RenderUnicodeRangePredicate(output, "isUnicode16LetterOrNumberBMP", letterOrNumberRanges);
    output.AppendLine();
    RenderUnicodeRangePredicate(output, "isUnicode16WhitespaceBMP", whitespaceRanges);
    return output.ToString();
}

static void RenderScalarMapping(StringBuilder output, string name, IReadOnlyList<ScalarMapping> mappings)
{
    output.Append("func ").Append(name).AppendLine("(r rune) rune {");
    output.AppendLine("\tswitch r {");
    foreach (var mapping in mappings)
    {
        output.Append("\tcase ").Append(GoRuneLiteral(mapping.Scalar)).Append(":\n\t\treturn ").Append(GoRuneLiteral(mapping.Mapping)).AppendLine();
    }

    output.AppendLine("\tdefault:");
    output.AppendLine("\t\treturn r");
    output.AppendLine("\t}");
    output.AppendLine("}");
}

static void RenderUnicodeRangePredicate(StringBuilder output, string name, IReadOnlyList<CodeUnitRange> ranges)
{
    output.Append("func ").Append(name).AppendLine("(r rune) bool {");
    output.AppendLine("\tswitch {");
    foreach (var range in ranges)
    {
        output.Append("\tcase r >= ").Append(GoRuneLiteral(range.First)).Append(" && r <= ").Append(GoRuneLiteral(range.Last)).AppendLine(":");
        output.AppendLine("\t\treturn true");
    }

    output.AppendLine("\tdefault:");
    output.AppendLine("\t\treturn false");
    output.AppendLine("\t}");
    output.AppendLine("}");
}

static List<ScalarMapping> BuildOrdinalIgnoreCaseMappings(out int groupCount)
{
    var representatives = new Dictionary<string, int>(StringComparer.OrdinalIgnoreCase);
    var mappings = new List<ScalarMapping>();
    for (var scalar = 0; scalar <= 0x10ffff; scalar++)
    {
        if (scalar is >= 0xd800 and <= 0xdfff)
        {
            continue;
        }

        var value = new Rune(scalar).ToString();
        if (representatives.TryGetValue(value, out var representative))
        {
            mappings.Add(new ScalarMapping(scalar, representative));
        }
        else
        {
            representatives.Add(value, scalar);
        }
    }

    groupCount = representatives.Count;
    return mappings;
}

static void AddDiacriticMapping(List<DiacriticMapping> mappings, int scalar, string input)
{
    var replacement = input.RemoveDiacritics();
    if (!string.Equals(input, replacement, StringComparison.Ordinal))
    {
        if (replacement.Any(char.IsSurrogate))
        {
            throw new InvalidOperationException($"Diacritics replacement for U+{scalar:X6} contains a UTF-16 surrogate");
        }

        mappings.Add(new DiacriticMapping(scalar, replacement));
    }
}

static string RenderDiacriticsGo(
    IReadOnlyList<DiacriticMapping> mappings,
    int bmpCount,
    int supplementaryCount,
    string mappingHash,
    string packageHash,
    string generatorHash,
    string projectHash,
    string oracleAssemblyHash)
{
    var output = new StringBuilder();
    output.AppendLine("// Code generated by tools/compat/itemnormoracle --generate-diacritics-go; DO NOT EDIT.");
    output.AppendLine("//");
    output.AppendLine($"// Source: Diacritics {DiacriticsVersion}, NuGet package SHA-256: {packageHash}.");
    output.AppendLine($"// Generator: tools/compat/itemnormoracle/Program.cs SHA-256: {generatorHash}.");
    output.AppendLine($"// Generator project: tools/compat/itemnormoracle/ItemNormOracle.csproj SHA-256: {projectHash}.");
    output.AppendLine($"// Generator assembly: ItemNormOracle.dll SHA-256: {oracleAssemblyHash}.");
    output.AppendLine("// Input domain: every non-surrogate BMP code unit and every supplementary Unicode scalar.");
    output.AppendLine($"// Changed mappings: {mappings.Count} total (BMP: {bmpCount}; supplementary: {supplementaryCount}).");
    output.AppendLine($"// Canonical mapping SHA-256: {mappingHash}.");
    output.AppendLine("// Surrogates and U+FFFD are intentionally not handled here; callers retain malformed-input policy.");
    output.AppendLine();
    output.AppendLine("package item");
    output.AppendLine();
    output.AppendLine("func diacriticsReplacement(r rune) string {");
    output.AppendLine("\tswitch r {");
    foreach (var mapping in mappings)
    {
        output.Append("\tcase ").Append(GoRuneLiteral(mapping.Scalar)).Append(":\n");
        output.Append("\t\treturn ").Append(GoStringLiteral(mapping.Replacement)).AppendLine();
    }

    output.AppendLine("\tdefault:");
    output.AppendLine("\t\treturn string(r)");
    output.AppendLine("\t}");
    output.AppendLine("}");
    return output.ToString();
}

static string GoRuneLiteral(int scalar) => scalar <= 0xffff ? $"'\\u{scalar:X4}'" : $"'\\U{scalar:X8}'";

static string GoStringLiteral(string value)
{
    var output = new StringBuilder("\"");
    foreach (var rune in value.EnumerateRunes())
    {
        output.Append(rune.Value switch
        {
            (int)'\\' => "\\\\",
            (int)'\"' => "\\\"",
            (int)'\n' => "\\n",
            (int)'\r' => "\\r",
            (int)'\t' => "\\t",
            <= 0x7f and >= 0x20 => ((char)rune.Value).ToString(),
            <= 0xffff => $"\\u{rune.Value:X4}",
            _ => $"\\U{rune.Value:X8}"
        });
    }

    return output.Append('\"').ToString();
}

static string FindRepositoryRoot(string vectorsPath)
{
    for (var directory = new DirectoryInfo(Path.GetDirectoryName(vectorsPath) ?? throw new InvalidOperationException("vectors has no parent directory")); directory is not null; directory = directory.Parent)
    {
        if (File.Exists(Path.Combine(directory.FullName, "upstream", "jellyfin", "Directory.Packages.props")))
        {
            return directory.FullName;
        }
    }

    throw new InvalidOperationException("could not locate repository root from vectors path");
}

static void ValidateAuthoritativeUpstream(string upstreamRoot)
{
    ValidateUpstreamCommit(upstreamRoot);
    ValidateUpstreamWorktree(upstreamRoot);
    ValidatePinnedUpstreamSources(upstreamRoot);
}

static void ValidateUpstreamCommit(string upstreamRoot)
{
    var actual = ReadGitHead(upstreamRoot);
    if (!string.Equals(actual, ExpectedCommit, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"upstream checkout must be pinned at {ExpectedCommit}, found {actual}");
    }
}

static void ValidateUpstreamWorktree(string upstreamRoot)
{
    var startInfo = new ProcessStartInfo("git")
    {
        WorkingDirectory = upstreamRoot,
        RedirectStandardOutput = true,
        RedirectStandardError = true,
        UseShellExecute = false
    };
    startInfo.ArgumentList.Add("status");
    startInfo.ArgumentList.Add("--porcelain=v1");
    startInfo.ArgumentList.Add("--untracked-files=all");
    startInfo.ArgumentList.Add("--ignored=no");

    using var process = Process.Start(startInfo)
        ?? throw new InvalidOperationException("could not start git to validate the upstream worktree");
    var outputTask = process.StandardOutput.ReadToEndAsync();
    var errorTask = process.StandardError.ReadToEndAsync();
    process.WaitForExit();
    var output = outputTask.GetAwaiter().GetResult();
    var error = errorTask.GetAwaiter().GetResult();
    if (process.ExitCode != 0)
    {
        throw new InvalidOperationException($"could not validate upstream worktree cleanliness: {error.Trim()}");
    }

    if (!string.IsNullOrEmpty(output))
    {
        throw new InvalidOperationException($"upstream worktree is dirty; pinned oracle inputs must have no tracked or untracked changes:\n{output.TrimEnd()}");
    }
}

static void ValidatePinnedUpstreamSources(string upstreamRoot)
{
    foreach (var source in GetPinnedUpstreamSources())
    {
        var path = Path.Combine(upstreamRoot, source.RelativePath);
        if (!File.Exists(path))
        {
            throw new FileNotFoundException($"required pinned oracle input is missing: {source.ProvenanceName}", path);
        }

        var actualHash = Sha256Bytes(File.ReadAllBytes(path));
        if (!string.Equals(actualHash, source.ExpectedSha256, StringComparison.Ordinal))
        {
            throw new InvalidOperationException($"pinned upstream source hash mismatch for {source.ProvenanceName}: expected {source.ExpectedSha256}, found {actualHash}");
        }
    }
}

static string ReadGitHead(string upstreamRoot)
{
    var gitDirectory = Path.Combine(upstreamRoot, ".git");
    var head = File.ReadAllText(Path.Combine(gitDirectory, "HEAD")).Trim();
    if (!head.StartsWith("ref: ", StringComparison.Ordinal))
    {
        return head;
    }

    var reference = head[5..];
    var looseReference = Path.Combine(gitDirectory, reference);
    if (File.Exists(looseReference))
    {
        return File.ReadAllText(looseReference).Trim();
    }

    var packedReferences = Path.Combine(gitDirectory, "packed-refs");
    if (File.Exists(packedReferences))
    {
        foreach (var line in File.ReadLines(packedReferences))
        {
            var parts = line.Split(' ', 2, StringSplitOptions.TrimEntries);
            if (parts.Length == 2 && parts[1] == reference)
            {
                return parts[0];
            }
        }
    }

    throw new InvalidOperationException($"cannot resolve upstream git reference {reference}");
}

static VectorManifest DeserializeVectors(string vectorsPath)
{
    var manifest = JsonSerializer.Deserialize<VectorManifest>(File.ReadAllText(vectorsPath), OracleJson.Options)
        ?? throw new InvalidOperationException("vectors document is empty");
    if (manifest.Format != "jellyfin-item-normalization-vectors/v1" || manifest.Cases is null || manifest.Cases.Count == 0)
    {
        throw new InvalidOperationException("vectors document is not jellyfin-item-normalization-vectors/v1 or has no cases");
    }

    return manifest;
}

static SortedDictionary<string, string> BuildSourceHashes(string repositoryRoot, string upstreamRoot, string vectorsPath)
{
    ValidatePinnedUpstreamSources(upstreamRoot);
    var paths = new[]
    {
        ("vectors", vectorsPath),
        ("tool", Path.Combine(repositoryRoot, "tools", "compat", "itemnormoracle", "Program.cs")),
        ("project", Path.Combine(repositoryRoot, "tools", "compat", "itemnormoracle", "ItemNormOracle.csproj"))
    };

    var result = new SortedDictionary<string, string>(StringComparer.Ordinal);
    foreach (var (name, path) in paths)
    {
        if (!File.Exists(path))
        {
            throw new FileNotFoundException($"required oracle input is missing: {name}", path);
        }

        result.Add(name, Sha256Bytes(File.ReadAllBytes(path)));
    }

    foreach (var source in GetPinnedUpstreamSources())
    {
        result.Add(source.ProvenanceName, source.ExpectedSha256);
    }

    result.Add("oracle-assembly", ExecutingOracleAssemblyHash());
    result.Add("loaded-jellyfin-extensions", ValidateTrustedLoadedAssembly("jellyfin-extensions", "Jellyfin.Extensions", JellyfinExtensionsAssemblySha256, typeof(StringExtensions).Assembly));
    result.Add("loaded-mediabrowser-controller", ValidateTrustedLoadedAssembly("mediabrowser-controller", "MediaBrowser.Controller", MediaBrowserControllerAssemblySha256, typeof(BaseItem).Assembly));
    result.Add("loaded-mediabrowser-model", ValidateTrustedLoadedAssembly("mediabrowser-model", "MediaBrowser.Model", MediaBrowserModelAssemblySha256, typeof(ServerConfiguration).Assembly));
    result.Add("loaded-system-private-corelib", ValidateTrustedLoadedAssembly("system-private-corelib", "System.Private.CoreLib", SystemPrivateCoreLibAssemblySha256, typeof(object).Assembly));
    result.Add("loaded-system-text-regular-expressions", ValidateTrustedLoadedAssembly("system-text-regular-expressions", "System.Text.RegularExpressions", SystemTextRegularExpressionsAssemblySha256, typeof(System.Text.RegularExpressions.Regex).Assembly));
    result.Add("loaded-system-text-json", ValidateTrustedLoadedAssembly("system-text-json", "System.Text.Json", SystemTextJsonAssemblySha256, typeof(JsonSerializer).Assembly));
    result.Add("loaded-system-text-encodings-web", ValidateTrustedLoadedAssembly("system-text-encodings-web", "System.Text.Encodings.Web", SystemTextEncodingsWebAssemblySha256, typeof(JavaScriptEncoder).Assembly));
    result.Add("loaded-system-io-compression", ValidateTrustedLoadedAssembly("system-io-compression", "System.IO.Compression", SystemIOCompressionAssemblySha256, typeof(ZipArchive).Assembly));
    result.Add("loaded-system-security-cryptography", ValidateTrustedLoadedAssembly("system-security-cryptography", "System.Security.Cryptography", SystemSecurityCryptographyAssemblySha256, typeof(SHA256).Assembly));
    foreach (var package in GetPinnedPackages())
    {
        var (archiveHash, loadedHash) = ValidatePackageAndLoadedAssembly(package);
        result.Add($"archive-{package.ProvenanceName}", archiveHash);
        result.Add($"loaded-{package.ProvenanceName}", loadedHash);
    }

    return result;
}

static IReadOnlyList<PinnedSourceInput> GetPinnedUpstreamSources() =>
[
    new("upstream-directory-packages", "Directory.Packages.props", "4c0f35e5eb8588c285ff272c417892fc2ec38197155774d86ceb63bc805c0519"),
    new("upstream-string-extensions", "src/Jellyfin.Extensions/StringExtensions.cs", "d827549f6f217bf0b6d313f401c16f50c2b44cb7ab1ca4bc787eb9a59065b239"),
    new("upstream-string-extension-tests", "tests/Jellyfin.Extensions.Tests/StringExtensionsTests.cs", "8dde405a94a754ae52f5323e95a625ec011dcc5452ecb925b882827dcacb6e91"),
    new("upstream-base-item", "MediaBrowser.Controller/Entities/BaseItem.cs", "8ba628f664c732bd926694f2bdbe561250a1233a102a19049f1496a2b50e8845"),
    new("upstream-base-item-tests", "tests/Jellyfin.Controller.Tests/Entities/BaseItemTests.cs", "225f072a5c3cde397f808fa3a06b33fc19c8c6537d49da3c14100b5c8ab93fe4"),
    new("upstream-server-configuration", "MediaBrowser.Model/Configuration/ServerConfiguration.cs", "9639d51fb6934c87a32b74f68d33d38369d4029bcfcebd100673334ef68785a3")
];

static string ExecutingOracleAssemblyHash()
{
    return LoadedAssemblyHash("ItemNormOracle", Assembly.GetExecutingAssembly());
}

static string LoadedAssemblyHash(string expectedName, Assembly assembly)
{
    if (!string.Equals(assembly.GetName().Name, expectedName, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"loaded assembly identity mismatch: expected {expectedName}, found {assembly.GetName().Name}");
    }

    if (string.IsNullOrWhiteSpace(assembly.Location) || !File.Exists(assembly.Location))
    {
        throw new InvalidOperationException($"loaded assembly {expectedName} has no readable Assembly.Location");
    }

    try
    {
        return Sha256Bytes(File.ReadAllBytes(assembly.Location));
    }
    catch (Exception exception) when (exception is IOException or UnauthorizedAccessException)
    {
        throw new InvalidOperationException($"loaded assembly {expectedName} has an unreadable Assembly.Location: {assembly.Location}", exception);
    }
}

static void ValidateTrustedBehaviorAssemblies()
{
    ValidateTrustedLoadedAssembly("jellyfin-extensions", "Jellyfin.Extensions", JellyfinExtensionsAssemblySha256, typeof(StringExtensions).Assembly);
    ValidateTrustedLoadedAssembly("mediabrowser-controller", "MediaBrowser.Controller", MediaBrowserControllerAssemblySha256, typeof(BaseItem).Assembly);
    ValidateTrustedLoadedAssembly("mediabrowser-model", "MediaBrowser.Model", MediaBrowserModelAssemblySha256, typeof(ServerConfiguration).Assembly);
    ValidateTrustedLoadedAssembly("system-private-corelib", "System.Private.CoreLib", SystemPrivateCoreLibAssemblySha256, typeof(object).Assembly);
    ValidateTrustedLoadedAssembly("system-text-regular-expressions", "System.Text.RegularExpressions", SystemTextRegularExpressionsAssemblySha256, typeof(System.Text.RegularExpressions.Regex).Assembly);
    ValidateTrustedLoadedAssembly("system-text-json", "System.Text.Json", SystemTextJsonAssemblySha256, typeof(JsonSerializer).Assembly);
    ValidateTrustedLoadedAssembly("system-text-encodings-web", "System.Text.Encodings.Web", SystemTextEncodingsWebAssemblySha256, typeof(JavaScriptEncoder).Assembly);
    ValidateTrustedLoadedAssembly("system-io-compression", "System.IO.Compression", SystemIOCompressionAssemblySha256, typeof(ZipArchive).Assembly);
    ValidateTrustedLoadedAssembly("system-security-cryptography", "System.Security.Cryptography", SystemSecurityCryptographyAssemblySha256, typeof(SHA256).Assembly);
}

static string ValidateTrustedLoadedAssembly(string provenanceName, string expectedName, string expectedHash, Assembly assembly)
{
    var actualHash = LoadedAssemblyHash(expectedName, assembly);
    if (!string.Equals(actualHash, expectedHash, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"loaded/source byte-hash mismatch for {provenanceName}: expected {expectedHash}, found {actualHash}");
    }

    return actualHash;
}

static IReadOnlyList<PackageInput> GetPinnedPackages() =>
[
    new("diacritics-4.1.8", "diacritics", DiacriticsVersion, "lib/net9.0/Diacritics.dll", "Diacritics", DiacriticsPackageSha256),
    new("icu4n-60.1.0-alpha.356", "icu4n", Icu4NVersion, "lib/netstandard2.0/ICU4N.dll", "ICU4N", Icu4NPackageSha256),
    new("icu4n-transliterator-60.1.0-alpha.356", "icu4n.transliterator", Icu4NTransliteratorVersion, "lib/netstandard2.0/ICU4N.Transliterator.dll", "ICU4N.Transliterator", Icu4NTransliteratorPackageSha256)
];

static (string ArchiveHash, string LoadedHash) ValidatePackageAndLoadedAssembly(PackageInput package)
{
    var archivePath = PackageArchivePath(package.PackageId, package.Version);
    if (!File.Exists(archivePath))
    {
        throw new FileNotFoundException($"required pinned NuGet archive is missing: {package.ProvenanceName}", archivePath);
    }

    var archiveBytes = File.ReadAllBytes(archivePath);
    var archiveHash = Sha256Bytes(archiveBytes);
    if (!string.Equals(archiveHash, package.TrustedArchiveSha256, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"NuGet archive hash mismatch for {package.ProvenanceName}: expected {package.TrustedArchiveSha256}, found {archiveHash}");
    }

    using var archive = new ZipArchive(new MemoryStream(archiveBytes, writable: false), ZipArchiveMode.Read);
    var entry = archive.GetEntry(package.AssemblyEntry)
        ?? throw new InvalidOperationException($"NuGet archive {package.ProvenanceName} does not contain {package.AssemblyEntry}");
    using var entryStream = entry.Open();
    using var expectedAssembly = new MemoryStream();
    entryStream.CopyTo(expectedAssembly);
    var expectedBytes = expectedAssembly.ToArray();
    var expectedHash = Sha256Bytes(expectedBytes);

    var loadedAssemblyPath = Path.Combine(AppContext.BaseDirectory, Path.GetFileName(package.AssemblyEntry));
    if (!File.Exists(loadedAssemblyPath))
    {
        throw new InvalidOperationException($"loaded/archive byte-hash mismatch for {package.ProvenanceName}: missing build-output assembly at {loadedAssemblyPath}");
    }

    var loadedBytes = File.ReadAllBytes(loadedAssemblyPath);
    var loadedHash = Sha256Bytes(loadedBytes);
    if (!loadedBytes.AsSpan().SequenceEqual(expectedBytes) || !string.Equals(loadedHash, expectedHash, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"loaded/archive byte-hash mismatch for {package.ProvenanceName}: archive {expectedHash}, loaded {loadedHash}");
    }

    var loadedAssembly = Assembly.LoadFrom(loadedAssemblyPath);
    if (string.IsNullOrWhiteSpace(loadedAssembly.Location) || !File.Exists(loadedAssembly.Location))
    {
        throw new InvalidOperationException($"loaded assembly {package.AssemblyName} has no readable Assembly.Location");
    }

    if (!string.Equals(Path.GetFullPath(loadedAssembly.Location), Path.GetFullPath(loadedAssemblyPath), StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"loaded assembly {package.AssemblyName} was not loaded from validated path {loadedAssemblyPath}");
    }

    if (!string.Equals(loadedAssembly.GetName().Name, package.AssemblyName, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"loaded assembly identity mismatch for {package.ProvenanceName}: expected {package.AssemblyName}, found {loadedAssembly.GetName().Name}");
    }

    var postLoadHash = Sha256Bytes(File.ReadAllBytes(loadedAssembly.Location));
    if (!string.Equals(postLoadHash, expectedHash, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"loaded/archive byte-hash mismatch for {package.ProvenanceName}: archive {expectedHash}, loaded {postLoadHash}");
    }

    return (archiveHash, loadedHash);
}

static string PackageArchivePath(string packageId, string version)
{
    var packageRoot = Environment.GetEnvironmentVariable("NUGET_PACKAGES");
    if (string.IsNullOrWhiteSpace(packageRoot))
    {
        packageRoot = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), ".nuget", "packages");
    }

    return Path.Combine(packageRoot, packageId, version, $"{packageId}.{version}.nupkg");
}

static OracleCase Execute(VectorCase vector)
{
    if (string.IsNullOrWhiteSpace(vector.Id) || string.IsNullOrWhiteSpace(vector.Operation) || string.IsNullOrWhiteSpace(vector.Source) || vector.Input.ValueKind != JsonValueKind.Object)
    {
        throw new InvalidOperationException("each vector requires id, operation, input, and source");
    }

    return vector.Operation switch
    {
        "remove-diacritics" => new OracleCase(vector.Id, vector.Operation, vector.Input.Clone(), JsonSerializer.SerializeToElement(ResolveText(vector.Input).RemoveDiacritics(), OracleJson.Options), true, vector.Source),
        "clean-name" => new OracleCase(vector.Id, vector.Operation, vector.Input.Clone(), JsonSerializer.SerializeToElement(ResolveText(vector.Input).GetCleanValue(), OracleJson.Options), true, vector.Source),
        "sort-name" => ExecuteSort(vector),
        _ => throw new InvalidOperationException($"unknown vector operation {vector.Operation}")
    };
}

static OracleCase ExecuteSort(VectorCase vector)
{
    var input = vector.Input;
    var item = RequiredProperty(input, "item");
    var options = RequiredProperty(input, "options");
    var kind = OptionalString(item, "kind") ?? "Video";
    var name = OptionalString(item, "name");
    var forcedSortName = OptionalString(item, "forcedSortName");
    var explicitSortName = OptionalString(item, "sortName");
    var configuration = new ServerConfiguration
    {
        SortRemoveWords = OptionalStrings(options, "removeWords") ?? new[] { "the", "a", "an" },
        SortRemoveCharacters = OptionalStrings(options, "removeCharacters") ?? new[] { ",", "&", "-", "{", "}", "'" },
        SortReplaceCharacters = OptionalStrings(options, "replaceCharacters") ?? new[] { ".", "+", "%" }
    };
    var enableAlphaNumericSorting = !string.Equals(kind, "Person", StringComparison.Ordinal);
    var sortSubject = !string.IsNullOrEmpty(forcedSortName) ? forcedSortName : name;
    var value = explicitSortName ?? BaseItem.GetSortName(sortSubject!, enableAlphaNumericSorting, configuration);
    var requiresIcu = OptionalBoolean(options, "requiresIcu");
    var output = JsonSerializer.SerializeToElement(new SortOutput(value, value is not null), OracleJson.Options);
    return new OracleCase(vector.Id!, vector.Operation!, input.Clone(), output, !requiresIcu, vector.Source!);
}

static string ResolveText(JsonElement input)
{
    if (input.TryGetProperty("text", out var text))
    {
        return text.GetString() ?? throw new InvalidOperationException("text input cannot be null");
    }

    if (input.TryGetProperty("utf16", out var utf16) && utf16.ValueKind == JsonValueKind.Array)
    {
        var codeUnits = new char[utf16.GetArrayLength()];
        var index = 0;
        foreach (var codeUnit in utf16.EnumerateArray())
        {
            var value = codeUnit.GetInt32();
            if (value is < char.MinValue or > char.MaxValue)
            {
                throw new InvalidOperationException("utf16 code units must be between 0 and 65535");
            }

            codeUnits[index++] = (char)value;
        }

        return new string(codeUnits);
    }

    throw new InvalidOperationException("normalization input requires text or utf16");
}

static JsonElement RequiredProperty(JsonElement value, string property)
    => value.TryGetProperty(property, out var result) ? result : throw new InvalidOperationException($"missing required property {property}");

static string? OptionalString(JsonElement value, string property)
    => value.TryGetProperty(property, out var result) && result.ValueKind != JsonValueKind.Null ? result.GetString() : null;

static string[]? OptionalStrings(JsonElement value, string property)
{
    if (!value.TryGetProperty(property, out var result))
    {
        return null;
    }

    if (result.ValueKind != JsonValueKind.Array)
    {
        throw new InvalidOperationException($"{property} must be an array");
    }

    return result.EnumerateArray().Select(entry => entry.GetString() ?? throw new InvalidOperationException($"{property} cannot contain null")).ToArray();
}

static bool OptionalBoolean(JsonElement value, string property)
    => value.TryGetProperty(property, out var result) && result.ValueKind == JsonValueKind.True;

static string Serialize<T>(T value, bool trailingNewline)
{
    var rendered = JsonSerializer.Serialize(value, OracleJson.Options);
    return trailingNewline ? rendered + "\n" : rendered;
}

static string Sha256Hex(string value) => Sha256Bytes(Encoding.UTF8.GetBytes(value));

static string Sha256Bytes(byte[] value) => Convert.ToHexString(SHA256.HashData(value)).ToLowerInvariant();


internal sealed record Arguments(OracleMode Mode, string? Vectors, string Output);

internal enum OracleMode
{
    GenerateOracle,
    GenerateDiacriticsGo,
    GenerateUnicodeGo
}

internal sealed record DiacriticMapping(int Scalar, string Replacement);

internal sealed record ScalarMapping(int Scalar, int Mapping);

internal sealed record CodeUnitRange(int First, int Last);

internal sealed record PackageInput(
    string ProvenanceName,
    string PackageId,
    string Version,
    string AssemblyEntry,
    string AssemblyName,
    string TrustedArchiveSha256);

internal sealed record PinnedSourceInput(
    string ProvenanceName,
    string RelativePath,
    string ExpectedSha256);

internal sealed class VectorManifest
{
    public string? Format { get; init; }

    public List<VectorCase>? Cases { get; init; }
}

internal sealed class VectorCase
{
    public string? Id { get; init; }

    public string? Operation { get; init; }

    public JsonElement Input { get; init; }

    public string? Source { get; init; }
}

internal sealed record Provenance(
    string UpstreamCommit,
    string UpstreamTreeHead,
    string DiacriticsVersion,
    string Icu4NTransliteratorVersion,
    SortedDictionary<string, string> SourceHashes);

internal sealed record OracleCase(
    string Id,
    string Operation,
    JsonElement Input,
    JsonElement Output,
    bool Supported,
    string Source);

internal sealed record SortOutput(string? Value, bool Present);

internal sealed record OracleDocument(
    string Format,
    Provenance Provenance,
    IReadOnlyList<OracleCase> Cases,
    string? Sha256);


internal static class OracleJson
{
    internal static readonly JsonSerializerOptions Options = new()
    {
        Encoder = JavaScriptEncoder.Default,
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = true,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull
    };
}