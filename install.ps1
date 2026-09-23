# pcbpilot installer for native Windows (Windows PowerShell 5.1 and PowerShell 7+)
# Usage: irm https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.ps1 | iex
#
# Mirrors install.sh: same env knobs, same "verify everything before touching an
# installed file" order, same stage-then-swap Skill replace with backup/restore.
# No top-level param() block so the script stays pipe-to-iex safe; options come
# from environment variables, and from arguments when it is run as a file.
#
# ENCODING: this file must stay pure ASCII with no BOM. Windows PowerShell 5.1
# decodes a BOM-less .ps1 using the system ANSI codepage (UTF-8 text would be
# mojibake under `powershell -File`), and a UTF-8 BOM survives
# `irm ... | iex` as a literal U+FEFF that makes the first token unparseable.
# Chinese EasyEDA menu labels are therefore written as \uXXXX and decoded at
# print time by Expand-Unicode.

# $args exists in a script scope and is absent at the interactive/global scope an
# `iex` runs in. Assign before wrapping: @(Get-Variable ...) would box an empty
# $args into a one-element array.
$EasyEdaInstallerArgv = @()
try {
    $EasyEdaInstallerRaw = Get-Variable -Name 'args' -Scope 0 -ValueOnly -ErrorAction Stop
    if ($null -ne $EasyEdaInstallerRaw) { $EasyEdaInstallerArgv = @($EasyEdaInstallerRaw) }
} catch {
    $EasyEdaInstallerArgv = @()
}

& {
    Set-StrictMode -Version Latest
    $ErrorActionPreference = 'Stop'
    # Invoke-WebRequest is an order of magnitude slower on 5.1 with the progress bar on.
    $ProgressPreference = 'SilentlyContinue'

    $Repo = 'zhuangzard/pcbpilot'
    $SkillName = 'pcbpilot'
    $BinaryAsset = 'pcbpilot_windows_amd64.exe'

    # -- helpers ---------------------------------------------------------------
    function Write-Step {
        param([string]$Message)
        Write-Host '[pcbpilot] ' -ForegroundColor Blue -NoNewline
        Write-Host $Message
    }
    function Write-Ok {
        param([string]$Message)
        Write-Host '  OK  ' -ForegroundColor Green -NoNewline
        Write-Host $Message
    }
    function Write-Warn {
        param([string]$Message)
        Write-Host '  !!  ' -ForegroundColor Yellow -NoNewline
        Write-Host $Message
    }
    function Write-Detail {
        param([string]$Message)
        Write-Host "      $Message"
    }
    # Fatal errors throw: a file run exits non-zero, an interactive `iex` prints
    # the error and keeps the session (never `exit`, which would close the host).
    function Stop-Install {
        param([string]$Message)
        throw "pcbpilot install failed: $Message"
    }

    # Decode \uXXXX escapes so the ASCII-only source can still print CJK labels.
    # Hand-rolled instead of a [regex] MatchEvaluator to keep 5.1 behaviour exact.
    function Expand-Unicode {
        param([string]$Text)
        $builder = New-Object System.Text.StringBuilder
        $index = 0
        while ($index -lt $Text.Length) {
            if (($index + 6) -le $Text.Length -and $Text[$index] -eq '\' -and $Text[$index + 1] -eq 'u' -and
                $Text.Substring($index + 2, 4) -match '^[0-9a-fA-F]{4}$') {
                [void]$builder.Append([char][Convert]::ToInt32($Text.Substring($index + 2, 4), 16))
                $index += 6
                continue
            }
            [void]$builder.Append($Text[$index])
            $index++
        }
        return $builder.ToString()
    }

    function Get-EnvValue {
        param([string]$Name)
        $value = [Environment]::GetEnvironmentVariable($Name)
        if ($null -eq $value) { return '' }
        return $value.Trim()
    }

    function Test-AbsolutePath {
        param([string]$Path)
        return ($Path -match '^[A-Za-z]:[\\/]' -or $Path -match '^\\\\[^\\]')
    }

    function Get-HomeDir {
        $home_ = Get-EnvValue 'USERPROFILE'
        if (-not $home_) { $home_ = Get-EnvValue 'HOME' }
        if (-not $home_) { Stop-Install 'Neither USERPROFILE nor HOME is set; pass PCBPILOT_INSTALL_DIR' }
        return $home_.TrimEnd('\', '/')
    }

    # Run a native command without letting 5.1 turn its stderr into a terminating
    # NativeCommandError, and return both the exit code and the stdout text.
    function Invoke-Native {
        param([string]$FilePath, [string[]]$Arguments = @())
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $raw = @()
        $code = -1
        try {
            $raw = @(& $FilePath @Arguments 2>&1)
            $code = $LASTEXITCODE
        } catch {
            $raw = @()
            $code = -1
        } finally {
            $ErrorActionPreference = $previous
        }
        $text = (($raw | Where-Object { $_ -isnot [System.Management.Automation.ErrorRecord] } |
            ForEach-Object { [string]$_ }) -join "`n")
        return [pscustomobject]@{ ExitCode = $code; Output = $text.Trim() }
    }

    # Single HTTP GET that reports the status code instead of throwing, so the
    # failure path can explain itself the way install.sh does.
    function Invoke-HttpGet {
        param(
            [string]$Uri,
            [hashtable]$Headers = @{},
            [string]$OutFile = '',
            [int]$TimeoutSec = 300
        )
        $parameters = @{ Uri = $Uri; UseBasicParsing = $true; TimeoutSec = $TimeoutSec; ErrorAction = 'Stop' }
        if ($Headers.Count -gt 0) { $parameters['Headers'] = $Headers }
        if ($OutFile) { $parameters['OutFile'] = $OutFile }
        try {
            $response = Invoke-WebRequest @parameters
            $status = 200
            $body = ''
            $final = $Uri
            if ($null -ne $response) {
                if ($response.PSObject.Properties['StatusCode']) { $status = [int]$response.StatusCode }
                if (-not $OutFile -and $response.PSObject.Properties['Content']) { $body = [string]$response.Content }
                if ($response.PSObject.Properties['BaseResponse'] -and $null -ne $response.BaseResponse) {
                    $base = $response.BaseResponse
                    # 5.1 exposes ResponseUri (HttpWebResponse); 7 exposes RequestMessage.RequestUri.
                    if ($base.PSObject.Properties['ResponseUri'] -and $null -ne $base.ResponseUri) {
                        $final = [string]$base.ResponseUri.AbsoluteUri
                    } elseif ($base.PSObject.Properties['RequestMessage'] -and $null -ne $base.RequestMessage) {
                        $final = [string]$base.RequestMessage.RequestUri.AbsoluteUri
                    }
                }
            }
            return [pscustomobject]@{ Ok = $true; Status = $status; Body = $body; FinalUri = $final; Error = '' }
        } catch {
            $status = 0
            $exception = $_.Exception
            if ($null -ne $exception -and $exception.PSObject.Properties['Response'] -and $null -ne $exception.Response) {
                try { $status = [int]$exception.Response.StatusCode } catch { $status = 0 }
            }
            if ($OutFile -and (Test-Path -LiteralPath $OutFile)) {
                Remove-Item -LiteralPath $OutFile -Force -ErrorAction SilentlyContinue
            }
            $message = ''
            if ($null -ne $exception) { $message = [string]$exception.Message }
            return [pscustomobject]@{ Ok = $false; Status = $status; Body = ''; FinalUri = ''; Error = $message }
        }
    }

    # -- environment / platform guards -----------------------------------------
    if ($PSVersionTable.PSVersion.Major -lt 5) {
        Stop-Install "Windows PowerShell 5.1 or newer is required (found $($PSVersionTable.PSVersion))"
    }
    $isWindowsHost = $true
    if (Test-Path -LiteralPath 'Variable:IsWindows') { $isWindowsHost = [bool](Get-Variable -Name 'IsWindows' -ValueOnly) }
    if (-not $isWindowsHost) {
        Stop-Install 'install.ps1 targets native Windows; use install.sh on macOS/Linux'
    }
    if (-not [Environment]::Is64BitOperatingSystem) {
        Stop-Install 'Only 64-bit Windows (amd64) is published; no pcbpilot_windows_arm/386 asset exists'
    }
    # 5.1 defaults to SSL3/TLS1.0 on older builds; GitHub only serves TLS 1.2+.
    try {
        [Net.ServicePointManager]::SecurityProtocol =
            [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch {
        Write-Warn 'Could not force TLS 1.2; downloads may fail on old Windows builds'
    }

    # -- options: env vars first, then arguments when run as a file ------------
    $Version = Get-EnvValue 'PCBPILOT_VERSION'
    $InstallDir = Get-EnvValue 'PCBPILOT_INSTALL_DIR'
    $InstallSkills = Get-EnvValue 'PCBPILOT_INSTALL_SKILLS'
    $SkillPreserve = (Get-EnvValue 'PCBPILOT_SKILL_PRESERVE') -eq '1'
    $AddToPath = (Get-EnvValue 'PCBPILOT_ADD_TO_PATH') -eq '1'

    $argv = @($EasyEdaInstallerArgv |
        Where-Object { $null -ne $_ -and ([string]$_).Trim() -ne '' } |
        ForEach-Object { ([string]$_).Trim() })
    for ($i = 0; $i -lt $argv.Count; $i++) {
        $option = $argv[$i]
        $needsValue = $false
        switch -Regex ($option) {
            '^(?i)-{1,2}addtopath$|^(?i)--add-to-path$' { $AddToPath = $true }
            '^(?i)-{1,2}preserve$' { $SkillPreserve = $true }
            '^(?i)-{1,2}version$' { $needsValue = $true }
            '^(?i)-{1,2}installdir$|^(?i)--install-dir$' { $needsValue = $true }
            '^(?i)-{1,2}skills$' { $needsValue = $true }
            '^(?i)-h$|^(?i)-{1,2}help$' {
                Write-Host 'Usage: install.ps1 [-Version <tag>] [-InstallDir <abs path>] [-Skills auto|none|codex,claude,agents] [-Preserve] [-AddToPath]'
                Write-Host 'Env:   PCBPILOT_VERSION PCBPILOT_INSTALL_DIR PCBPILOT_INSTALL_SKILLS PCBPILOT_SKILL_PRESERVE'
                Write-Host '       PCBPILOT_ADD_TO_PATH PCBPILOT_GITHUB_PROXY CODEX_HOME CLAUDE_CONFIG_DIR GITHUB_TOKEN/GH_TOKEN'
                return
            }
            default { Stop-Install "Unknown option: $option (try -Help)" }
        }
        if ($needsValue) {
            if ($i + 1 -ge $argv.Count) { Stop-Install "$option needs a value" }
            $value = $argv[$i + 1]
            $i++
            if ($option -match '(?i)version') { $Version = $value.Trim() }
            elseif ($option -match '(?i)skills') { $InstallSkills = $value.Trim() }
            else { $InstallDir = $value.Trim() }
        }
    }

    # -- resolve the release tag -----------------------------------------------
    function Get-GitHubToken {
        $token = Get-EnvValue 'GITHUB_TOKEN'
        if (-not $token) { $token = Get-EnvValue 'GH_TOKEN' }
        if (-not $token) {
            $gh = Get-Command -Name 'gh' -CommandType Application -ErrorAction SilentlyContinue
            if ($null -ne $gh) {
                $result = Invoke-Native -FilePath (@($gh)[0].Source) -Arguments @('auth', 'token')
                if ($result.ExitCode -eq 0 -and $result.Output) { $token = $result.Output.Trim() }
            }
        }
        return $token
    }

    function Stop-RateLimited {
        param([int]$Status, [string]$Token)
        Write-Host ''
        Write-Warn "Unauthenticated api.github.com allows 60 requests/hour per IP;"
        Write-Detail 'a shared office / NAT / CI address burns through that fast.'
        if ($Token) { Write-Detail 'A token was sent but still rejected - it may be expired or invalid.' }
        Write-Detail 'Fix it either way:'
        Write-Detail '  1) authenticate (5000 requests/hour):'
        Write-Detail '       $env:GITHUB_TOKEN = "<token>"   # GH_TOKEN works too'
        Write-Detail '       gh auth login                   # gh CLI is picked up automatically'
        Write-Detail '  2) skip the API by pinning a release tag:'
        Write-Detail '       $env:PCBPILOT_VERSION = "<tag>"; irm .../install.ps1 | iex'
        Write-Detail "       tags: https://github.com/$Repo/releases"
        Stop-Install "GitHub API rate limit (HTTP $Status) - could not resolve the latest release"
    }

    function Resolve-LatestFromWeb {
        $result = Invoke-HttpGet -Uri "https://github.com/$Repo/releases/latest" -TimeoutSec 60
        if (-not $result.Ok -or $result.Status -ne 200) { return '' }
        $tag = ($result.FinalUri -split '/')[-1]
        if ($tag -match '^v[0-9]+\.[0-9]+\.[0-9]+$') { return $tag }
        return ''
    }

    if ($Version) {
        # Tags are v-prefixed; accept "1.5.2" as well as "v1.5.2".
        if ($Version -match '^[0-9]') { $Version = "v$Version" }
        Write-Step "Pinned release: $Version (PCBPILOT_VERSION)"
    } else {
        Write-Step 'Fetching latest release...'
        $token = Get-GitHubToken
        $headers = @{ 'Accept' = 'application/vnd.github+json' }
        if ($token) { $headers['Authorization'] = "Bearer $token" }
        $api = Invoke-HttpGet -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers $headers -TimeoutSec 60
        switch ($api.Status) {
            200 { }
            401 { Stop-Install 'GitHub API rejected the token (HTTP 401). Clear GITHUB_TOKEN/GH_TOKEN or run "gh auth login", or set PCBPILOT_VERSION=<tag>.' }
            404 { Stop-Install "No 'latest' release for $Repo (HTTP 404). Pick a tag from https://github.com/$Repo/releases and set PCBPILOT_VERSION=<tag>." }
            0 { Stop-Install "Could not reach api.github.com ($($api.Error)). Retry, or set PCBPILOT_VERSION=<tag> to skip the API." }
            default {
                if ($api.Status -eq 403 -or $api.Status -eq 429) {
                    $Version = Resolve-LatestFromWeb
                    if ($Version) {
                        Write-Warn "GitHub API returned HTTP $($api.Status); resolved latest from the public release redirect"
                    } else {
                        Stop-RateLimited -Status $api.Status -Token $token
                    }
                } else {
                    Stop-Install "GitHub API returned HTTP $($api.Status) while resolving the latest release. Set PCBPILOT_VERSION=<tag> to skip the API."
                }
            }
        }
        if (-not $Version) {
            try {
                $Version = [string](ConvertFrom-Json $api.Body).tag_name
            } catch {
                $Version = ''
            }
            if (-not $Version) { Stop-Install 'Could not parse a tag_name out of the GitHub API response. Set PCBPILOT_VERSION=<tag> to skip the API.' }
        }
        Write-Step "Latest: $Version"
    }
    $BareVersion = $Version -replace '^v', ''
    $BaseUrl = "https://github.com/$Repo/releases/download/$Version"

    # -- install dir -----------------------------------------------------------
    if ($InstallDir) {
        if (-not (Test-AbsolutePath $InstallDir)) { Stop-Install 'PCBPILOT_INSTALL_DIR must be an absolute path' }
        $InstallDir = $InstallDir.TrimEnd('\', '/')
    } else {
        $InstallDir = Join-Path (Get-HomeDir) '.local\bin'
    }
    if (-not (Test-Path -LiteralPath $InstallDir)) {
        [void](New-Item -ItemType Directory -Path $InstallDir -Force)
    }

    $TempRoot = Join-Path ([IO.Path]::GetTempPath()) ('easyeda-install-' + [Guid]::NewGuid().ToString('N'))
    [void](New-Item -ItemType Directory -Path $TempRoot -Force)
    try {
        # -- checksums first; nothing installed is touched until every asset
        # -- downloaded here has been verified.
        $checksumPath = Join-Path $TempRoot 'checksums.txt'
        $sums = Invoke-HttpGet -Uri "$BaseUrl/checksums.txt" -OutFile $checksumPath -TimeoutSec 120
        $HaveChecksums = $false
        if ($sums.Ok -and $sums.Status -eq 200) {
            $HaveChecksums = $true
        } elseif ($sums.Status -eq 404) {
            Write-Warn 'Old release has no checksums.txt; binary version and Skill metadata will be checked'
        } elseif ($sums.Status -eq 0) {
            Stop-Install "Could not download checksums.txt ($($sums.Error)); nothing installed"
        } else {
            Stop-Install "checksums.txt returned HTTP $($sums.Status); nothing installed"
        }

        $ExpectedSums = @{}
        if ($HaveChecksums) {
            foreach ($line in [IO.File]::ReadAllLines($checksumPath)) {
                $match = [regex]::Match($line.Trim(), '^([0-9a-fA-F]{64})\s+\*?(\S+)$')
                if ($match.Success) {
                    $name = $match.Groups[2].Value
                    if ($ExpectedSums.ContainsKey($name)) {
                        $ExpectedSums[$name] = ''   # duplicate -> reject on use
                    } else {
                        $ExpectedSums[$name] = $match.Groups[1].Value.ToLowerInvariant()
                    }
                }
            }
        }

        function Test-AssetChecksum {
            param([string]$Name, [string]$Path)
            if (-not $HaveChecksums) { return }
            if (-not $ExpectedSums.ContainsKey($Name) -or -not $ExpectedSums[$Name]) {
                Stop-Install "Missing/duplicate/invalid checksum for $Name"
            }
            $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($actual -ne $ExpectedSums[$Name]) { Stop-Install "checksum mismatch for $Name; nothing installed" }
            Write-Ok "sha256 verified: $Name"
        }

        function Get-ReleaseAsset {
            param([string]$Name, [string]$Destination)
            $primary = "$BaseUrl/$Name"
            for ($attempt = 1; $attempt -le 3; $attempt++) {
                $result = Invoke-HttpGet -Uri $primary -OutFile $Destination
                if ($result.Ok -and (Test-Path -LiteralPath $Destination)) { return }
                Write-Warn "GitHub download attempt $attempt/3 failed for $Name"
                if (Test-Path -LiteralPath $Destination) { Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue }
            }
            # A third-party transport is trusted only for availability. The expected
            # digest must already have come directly from GitHub.
            if (-not $HaveChecksums) {
                Stop-Install 'GitHub download failed and no trusted checksum is available; mirror fallback refused'
            }
            $configured = [Environment]::GetEnvironmentVariable('PCBPILOT_GITHUB_PROXY')
            if ($null -eq $configured) { $proxy = 'https://gh-proxy.com/' } else { $proxy = $configured.Trim() }
            if (-not $proxy -or $proxy -match '^(?i)off$') {
                Stop-Install "download failed: $primary (mirror fallback disabled)"
            }
            if ($proxy -like '*{url}*') {
                $mirror = $proxy.Replace('{url}', $primary)
            } else {
                $mirror = $proxy.TrimEnd('/') + '/' + $primary
            }
            Write-Warn "GitHub failed; trying checksum-verified mirror for $Name"
            $result = Invoke-HttpGet -Uri $mirror -OutFile $Destination
            if (-not $result.Ok -or -not (Test-Path -LiteralPath $Destination)) {
                Stop-Install "GitHub and mirror downloads both failed for $Name"
            }
        }

        # -- CLI binary: download, verify, and prove it runs here --------------
        Write-Step "Downloading $BinaryAsset..."
        $binaryTemp = Join-Path $TempRoot 'pcbpilot.exe'
        Get-ReleaseAsset -Name $BinaryAsset -Destination $binaryTemp
        Test-AssetChecksum -Name $BinaryAsset -Path $binaryTemp
        $probe = Invoke-Native -FilePath $binaryTemp -Arguments @('--version')
        if ($probe.ExitCode -ne 0) { Stop-Install 'Downloaded binary cannot run on this host; nothing installed' }
        if ($probe.Output -ne "pcbpilot $Version") {
            Stop-Install "Downloaded binary version differs: $($probe.Output); expected pcbpilot $Version"
        }
        Write-Ok "binary reports $($probe.Output)"

        # -- skill targets -----------------------------------------------------
        function Get-ClientBaseDir {
            param([string]$Client)
            switch ($Client) {
                'codex' {
                    $root = Get-EnvValue 'CODEX_HOME'
                    if (-not $root) { $root = Join-Path (Get-HomeDir) '.codex' }
                    return (Join-Path $root.TrimEnd('\', '/') 'skills')
                }
                'claude' {
                    $root = Get-EnvValue 'CLAUDE_CONFIG_DIR'
                    if (-not $root) { $root = Join-Path (Get-HomeDir) '.claude' }
                    return (Join-Path $root.TrimEnd('\', '/') 'skills')
                }
                'agents' { return (Join-Path (Join-Path (Get-HomeDir) '.agents') 'skills') }
                default { return '' }
            }
        }

        function Get-SkillTargets {
            if ($InstallSkills -and $InstallSkills -match '^(?i)none$') { return @() }
            if ($InstallSkills -and $InstallSkills -notmatch '^(?i)auto$') {
                return @($InstallSkills -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
            }
            $found = @()
            $codexHome = Get-EnvValue 'CODEX_HOME'
            if (-not $codexHome) { $codexHome = Join-Path (Get-HomeDir) '.codex' }
            if ((Test-Path -LiteralPath $codexHome) -or
                $null -ne (Get-Command -Name 'codex' -ErrorAction SilentlyContinue)) { $found += 'codex' }
            $claudeHome = Get-EnvValue 'CLAUDE_CONFIG_DIR'
            if (-not $claudeHome) { $claudeHome = Join-Path (Get-HomeDir) '.claude' }
            if ((Test-Path -LiteralPath $claudeHome) -or
                $null -ne (Get-Command -Name 'claude' -ErrorAction SilentlyContinue)) { $found += 'claude' }
            if (Test-Path -LiteralPath (Join-Path (Get-HomeDir) '.agents')) { $found += 'agents' }
            if ($found.Count -eq 0) {
                # Neither detected -> create both by default so the skill is ready when
                # a client shows up. PCBPILOT_INSTALL_SKILLS=none opts out.
                Write-Warn 'No Codex/Claude Code client detected; creating both skill dirs by default.'
                $found = @('codex', 'claude')
            }
            return $found
        }

        $Targets = @(Get-SkillTargets)
        # Reject bad client names/paths before changing the CLI or any Skill directory.
        foreach ($client in $Targets) {
            $base = Get-ClientBaseDir $client
            if (-not $base) { Stop-Install "Unknown skill target: $client" }
            if (-not (Test-AbsolutePath $base)) { Stop-Install "Client config directory must be absolute: $base" }
        }

        $SourceSkill = ''
        if ($Targets.Count -eq 0) {
            Write-Step 'Skill install skipped (PCBPILOT_INSTALL_SKILLS=none)'
        } else {
            Write-Step 'Downloading skills.tar.gz...'
            $archive = Join-Path $TempRoot 'skills.tar.gz'
            Get-ReleaseAsset -Name 'skills.tar.gz' -Destination $archive
            Test-AssetChecksum -Name 'skills.tar.gz' -Path $archive
            # bsdtar ships in %SystemRoot%\System32 on Windows 10 1803+. Prefer it
            # explicitly so a Git-Bash/MSYS tar earlier on PATH cannot change the
            # extraction semantics.
            $tarExe = Join-Path (Join-Path (Get-EnvValue 'SystemRoot') 'System32') 'tar.exe'
            if (-not (Test-Path -LiteralPath $tarExe)) {
                $candidate = Get-Command -Name 'tar.exe' -CommandType Application -ErrorAction SilentlyContinue
                if ($null -eq $candidate) {
                    Stop-Install 'tar.exe not found (ships with Windows 10 1803+). Update Windows, or extract skills.tar.gz manually and run "pcbpilot update --skill-only --create-missing".'
                }
                $tarExe = (@($candidate)[0]).Source
            }
            $extract = Invoke-Native -FilePath $tarExe -Arguments @('-xzf', $archive, '-C', $TempRoot)
            if ($extract.ExitCode -ne 0) { Stop-Install 'Invalid Skill archive; nothing installed' }
            $SourceSkill = Join-Path $TempRoot $SkillName
            $skillMd = Join-Path $SourceSkill 'SKILL.md'
            if (-not (Test-Path -LiteralPath $skillMd) -or (Get-Item -LiteralPath $skillMd).Length -eq 0) {
                Stop-Install 'Skill archive has no SKILL.md; nothing installed'
            }
            $metaMatch = [regex]::Match([IO.File]::ReadAllText($skillMd), '(?m)^  version:\s*"([^"]*)"\s*$')
            if (-not $metaMatch.Success -or $metaMatch.Groups[1].Value -ne $BareVersion) {
                Stop-Install "Skill metadata version differs from $Version; nothing installed"
            }
            Write-Ok "skill archive metadata.version = $BareVersion"
        }

        # -- install the CLI (handle a running/locked pcbpilot.exe) -------------
        $target = Join-Path $InstallDir 'pcbpilot.exe'
        $staged = Join-Path $InstallDir ('.easyeda-download-' + [IO.Path]::GetRandomFileName() + '.exe')
        Copy-Item -LiteralPath $binaryTemp -Destination $staged -Force
        $restartNeeded = $false
        $orphan = ''
        try {
            Move-Item -LiteralPath $staged -Destination $target -Force
        } catch {
            # Windows refuses to overwrite a mapped executable but does allow renaming
            # it, so move the running file aside and drop the new one in its place.
            if (-not (Test-Path -LiteralPath $target)) {
                Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
                Stop-Install "Could not write $target ($($_.Exception.Message)); nothing installed"
            }
            $aside = Join-Path $InstallDir ('.pcbpilot-old-' + [DateTime]::Now.ToString('yyyyMMddHHmmss') + '.exe')
            try {
                Move-Item -LiteralPath $target -Destination $aside -Force
            } catch {
                Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
                Stop-Install "$target is locked and could not be renamed aside; stop the daemon (pcbpilot daemon stop) and re-run. Nothing installed."
            }
            try {
                Move-Item -LiteralPath $staged -Destination $target -Force
            } catch {
                Move-Item -LiteralPath $aside -Destination $target -Force
                Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
                Stop-Install "Could not install $target; the previous binary was restored"
            }
            $restartNeeded = $true
            Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $aside) { $orphan = $aside }
        }
        Write-Ok "CLI installed -> $target"
        if ($restartNeeded) {
            Write-Warn 'The previous pcbpilot.exe was in use; it was replaced by renaming it aside.'
            Write-Detail 'Restart the daemon so it runs the new binary: pcbpilot daemon stop; pcbpilot daemon start'
            if ($orphan) { Write-Detail "Delete the old file once the process exits: $orphan" }
        }
        # Best-effort sweep of files an earlier locked upgrade had to leave behind.
        Get-ChildItem -LiteralPath $InstallDir -Filter '.pcbpilot-old-*.exe' -Force -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -ne $orphan } |
            ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }

        # -- install the Skill (stage -> swap, restore the backup on failure) ---
        function Install-SkillTo {
            param([string]$Client, [string]$Source)
            $base = Get-ClientBaseDir $Client
            if (-not $base) { Stop-Install "Unknown skill target: $Client" }
            if (-not (Test-Path -LiteralPath $base)) { [void](New-Item -ItemType Directory -Path $base -Force) }
            $dest = Join-Path $base $SkillName
            if (Test-Path -LiteralPath $dest) {
                $existing = Get-Item -LiteralPath $dest -Force
                if ($existing.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                    $linkTarget = ''
                    try {
                        $value = $existing.Target
                        if ($value) { $linkTarget = [string](@($value)[0]) }
                    } catch {
                        $linkTarget = ''
                    }
                    if (-not $linkTarget -or -not (Test-AbsolutePath $linkTarget)) {
                        Stop-Install "$dest is a symlink/junction whose target cannot be resolved; remove it or set a different client config dir"
                    }
                    $dest = $linkTarget.TrimEnd('\', '/')
                    $base = Split-Path -Parent $dest
                }
            }
            $stage = Join-Path $base ('.easyeda-stage.' + [IO.Path]::GetRandomFileName())
            $backup = "$stage.previous"
            [void](New-Item -ItemType Directory -Path $stage -Force)
            try {
                Copy-Item -Path (Join-Path $Source '*') -Destination $stage -Recurse -Force
            } catch {
                Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
                Stop-Install "Could not stage $Client Skill; existing files kept"
            }
            $preserved = $false
            if ($SkillPreserve -and (Test-Path -LiteralPath $dest)) {
                try {
                    Copy-Item -Path (Join-Path $dest '*') -Destination $stage -Recurse -Force
                } catch {
                    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
                    Stop-Install "Could not preserve $Client Skill; existing files kept"
                }
                $preserved = $true
            } else {
                # LF-terminated, byte-identical to what install.sh and `pcbpilot update` write.
                [IO.File]::WriteAllText((Join-Path $stage '.version'), "$BareVersion`n")
            }
            if (Test-Path -LiteralPath $dest) {
                try {
                    Move-Item -LiteralPath $dest -Destination $backup -Force
                } catch {
                    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
                    Stop-Install "Could not back up $Client Skill; existing files kept"
                }
            }
            try {
                Move-Item -LiteralPath $stage -Destination $dest -Force
            } catch {
                if (Test-Path -LiteralPath $backup) {
                    try { Move-Item -LiteralPath $backup -Destination $dest -Force } catch { Stop-Install "Restore $backup to $dest" }
                }
                Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
                Stop-Install "Could not install $Client Skill"
            }
            if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup -Recurse -Force -ErrorAction SilentlyContinue }
            if ($preserved) {
                Write-Warn "$Client Skill preserved (mixed local/release content; previous version marker kept) -> $dest"
            } else {
                Write-Ok "$Client Skill installed -> $dest"
            }
        }

        foreach ($client in $Targets) {
            Install-SkillTo -Client $client -Source $SourceSkill
        }

        # -- PATH --------------------------------------------------------------
        function Test-PathContains {
            param([string]$PathValue, [string]$Directory)
            if (-not $PathValue) { return $false }
            $wanted = $Directory.TrimEnd('\', '/')
            foreach ($entry in ($PathValue -split ';')) {
                if ($entry -and $entry.Trim().TrimEnd('\', '/') -eq $wanted) { return $true }
            }
            return $false
        }

        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
        if (-not (Test-PathContains $userPath $InstallDir) -and -not (Test-PathContains $machinePath $InstallDir)) {
            if ($AddToPath) {
                $current = $userPath
                if ($null -eq $current) { $current = '' }
                $updated = $current.TrimEnd(';')
                if ($updated) { $updated = "$updated;$InstallDir" } else { $updated = $InstallDir }
                # User scope only; the machine PATH is never touched.
                [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
                $env:Path = "$env:Path;$InstallDir"
                Write-Ok "added to the user PATH -> $InstallDir (open a new terminal for other apps to see it)"
            } else {
                Write-Warn "$InstallDir is not in your user PATH"
                Write-Detail 'Add it (user scope only, no admin needed):'
                Write-Detail ("  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path','User') + ';{0}', 'User')" -f $InstallDir)
                Write-Detail 'Or re-run this installer with -AddToPath (file) / $env:PCBPILOT_ADD_TO_PATH=1 (pipe).'
                Write-Host ''
            }
        }

        # -- next steps --------------------------------------------------------
        # EasyEDA's menu labels are Chinese, but this file must stay pure ASCII
        # (see the encoding note at the top), so they are written as \uXXXX and
        # decoded here. Every line also carries the English label, which still
        # reads correctly on a console whose codepage cannot render CJK.
        $ExtManage = Expand-Unicode '\u6269\u5c55\u7ba1\u7406'
        $ImportExt = Expand-Unicode '\u5bfc\u5165\u6269\u5c55'
        $Marketplace = Expand-Unicode '\u7acb\u521b\u5b98\u65b9\u63d2\u4ef6\u5e02\u573a'
        $AllowExternal = Expand-Unicode '\u5141\u8bb8\u5916\u90e8\u4ea4\u4e92'
        $Advanced = Expand-Unicode '\u9ad8\u7ea7'
        $ExtManager = Expand-Unicode '\u6269\u5c55\u7ba1\u7406\u5668'
        $Installed = Expand-Unicode '\u5df2\u5b89\u88c5'

        Write-Host ''
        Write-Ok "pcbpilot $Version installed"
        Write-Host ''
        Write-Host 'Next steps:'
        Write-Host '  1. Start the daemon:'
        Write-Host '       pcbpilot daemon start'
        Write-Host ''
        Write-Host '  2. Install the EasyEDA connector extension (either channel):'
        Write-Host '     a) Sideload this release (same major.minor compatibility line):'
        Write-Host "          Download: $BaseUrl/pcbpilot-connector.eext"
        Write-Host "          In EasyEDA Pro: $ExtManage (Extensions) -> $ImportExt (Import extension) -> pick the .eext"
        Write-Host "     b) $Marketplace (LCEDA marketplace: one-click, auto-updates in place; may lag the CLI):"
        Write-Host '          https://github.com/zhuangzard/pcbpilot/releases/latest'
        Write-Host ''
        Write-Host "  3. In EasyEDA Pro: enable $AllowExternal (Allow external interaction)"
        Write-Host "       V3.2 desktop: $Advanced (Advanced) -> $ExtManager (Extension manager) -> $Installed (Installed)"
        Write-Host '       -> select the connector. The Config tab only appears while the extension'
        Write-Host "       shows Enabled, and the $AllowExternal checkbox lives on that Config tab."
        Write-Host ''
        Write-Host '  4. Use the skill in your AI client:'
        Write-Host '       /pcbpilot       (schematic + PCB workflow)'
        if ($Targets.Count -gt 0) {
            Write-Host "       Installed for: $($Targets -join ', ')"
        }
        Write-Host ''
        Write-Host 'Upgrading later? No need to re-run this script:'
        Write-Host '       pcbpilot update           # CLI binary + skill dirs -> latest'
        Write-Host '       pcbpilot update --check   # report only (cli / skill / connector)'
        Write-Host '     (connector patch drift is compatible; re-import only when `update` reports a major/minor mismatch)'
        Write-Host ''
        Write-Host "Full docs: https://github.com/$Repo"
    } finally {
        if (Test-Path -LiteralPath $TempRoot) {
            Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
