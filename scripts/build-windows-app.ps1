[CmdletBinding()]
param(
 [Parameter(Mandatory=$true)][string]$OutputDirectory,
 [string]$Version=$env:APP_VERSION,
 [string]$Commit=$env:APP_COMMIT,
 [string]$GoBinary='go',
 [string]$CacheDirectory=(Join-Path $PSScriptRoot '..\.tools\native-cache'),
 [switch]$SkipFrontend
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if($env:OS -ne 'Windows_NT'){throw 'The Windows amd64 package must be assembled and checked on Windows.'}
if($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$' -or $Commit -notmatch '^[a-f0-9]{40}$'){throw 'Stable Version and full Commit are required.'}
$Root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$OutputDirectory=[IO.Path]::GetFullPath($OutputDirectory)
$CacheDirectory=[IO.Path]::GetFullPath($CacheDirectory)
[IO.Directory]::CreateDirectory($OutputDirectory)|Out-Null
[IO.Directory]::CreateDirectory($CacheDirectory)|Out-Null
$Build=Join-Path $CacheDirectory ('build-'+[guid]::NewGuid().ToString('N'))
$Components=Join-Path $Build 'components'
[IO.Directory]::CreateDirectory($Components)|Out-Null
$NodeVersion='22.23.3'
$NodeHash='2b0ff57b049cda1bbcea2240eec20467018713c1efe1f7360c2681859b90ed71'
$NodeZip=Join-Path $CacheDirectory "node-v$NodeVersion-win-x64.zip"
if(!(Test-Path -LiteralPath $NodeZip)){Invoke-WebRequest -UseBasicParsing -Uri "https://nodejs.org/dist/v$NodeVersion/node-v$NodeVersion-win-x64.zip" -OutFile $NodeZip}
if((Get-FileHash -LiteralPath $NodeZip -Algorithm SHA256).Hash.ToLowerInvariant() -ne $NodeHash){throw 'Pinned Node archive checksum mismatch.'}
$NodeExpanded=Join-Path $Build 'node-download'
Add-Type -AssemblyName System.IO.Compression.FileSystem
[IO.Directory]::CreateDirectory($NodeExpanded)|Out-Null
$NodeArchive=[IO.Compression.ZipFile]::OpenRead($NodeZip)
try{foreach($filename in @('node.exe','LICENSE')){$entry=$NodeArchive.GetEntry("node-v$NodeVersion-win-x64/$filename");if(!$entry){throw 'Pinned Node package is incomplete'};$dest=Join-Path $NodeExpanded "node-v$NodeVersion-win-x64\$filename";[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($dest))|Out-Null;[IO.Compression.ZipFileExtensions]::ExtractToFile($entry,$dest)}}finally{$NodeArchive.Dispose()}
$Node=Join-Path $Components 'node'
[IO.Directory]::CreateDirectory($Node)|Out-Null
Copy-Item -LiteralPath (Join-Path $NodeExpanded "node-v$NodeVersion-win-x64\node.exe") -Destination $Node
Copy-Item -LiteralPath (Join-Path $NodeExpanded "node-v$NodeVersion-win-x64\LICENSE") -Destination $Node
$OriginalPath=$env:PATH
$OriginalGoOS=$env:GOOS;$OriginalGoArch=$env:GOARCH;$OriginalCGO=$env:CGO_ENABLED
try {
 $env:PATH="$Node;$OriginalPath"
 $env:GOOS='windows';$env:GOARCH='amd64';$env:CGO_ENABLED='0'
 Push-Location $Root
 try {
  if(!$SkipFrontend){Push-Location frontend;try{& npm.cmd ci;if($LASTEXITCODE -ne 0){throw 'frontend npm ci failed'};& npm.cmd run sync;if($LASTEXITCODE -ne 0){throw 'frontend sync failed'}}finally{Pop-Location}}
  $Flags="-s -w -X github.com/caigee-cmd/cli2api/internal/buildinfo.Version=$Version -X github.com/caigee-cmd/cli2api/internal/buildinfo.Commit=$Commit"
  & $GoBinary build -trimpath -ldflags $Flags -o (Join-Path $Build 'qoder-api-proxy.exe') ./cmd/server
  if($LASTEXITCODE -ne 0){throw 'gateway build failed'}
  & $GoBinary build -trimpath -ldflags $Flags -o (Join-Path $Components 'updater.exe') ./cmd/updater
  if($LASTEXITCODE -ne 0){throw 'updater build failed'}
 }finally{Pop-Location}
 $Worker=Join-Path $Components 'worker';[IO.Directory]::CreateDirectory($Worker)|Out-Null
 foreach($name in @('package.json','package-lock.json','NOTICE','last-plain.sample.json')){Copy-Item -LiteralPath (Join-Path $Root "worker\$name") -Destination $Worker}
 Copy-Item -LiteralPath (Join-Path $Root 'worker\src') -Destination $Worker -Recurse
 & npm.cmd ci --omit=dev --ignore-scripts --no-audit --no-fund --fetch-retries=1 --fetch-timeout=30000 --prefix $Worker
 if($LASTEXITCODE -ne 0){throw 'worker dependency install failed'}
 $Cli=Join-Path $Components 'cli';[IO.Directory]::CreateDirectory($Cli)|Out-Null
 foreach($name in @('package.json','package-lock.json')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot "windows-runtime\$name") -Destination $Cli}
 & npm.cmd ci --omit=dev --ignore-scripts --no-audit --no-fund --fetch-retries=1 --fetch-timeout=30000 --prefix $Cli
 if($LASTEXITCODE -ne 0){throw 'pinned CLI dependency install failed'}
 & (Join-Path $Node 'node.exe') (Join-Path $PSScriptRoot 'verify-windows-runtime.mjs') $Build
 if($LASTEXITCODE -ne 0){throw 'Windows runtime compatibility smoke failed'}
 Copy-Item -LiteralPath (Join-Path $Root 'LICENSE') -Destination (Join-Path $Components 'LICENSE-project.txt')
 $Files=@(Get-ChildItem -LiteralPath $Build -Recurse -File | Where-Object {!$_.FullName.StartsWith($NodeExpanded+"\",[StringComparison]::OrdinalIgnoreCase)} | Sort-Object FullName | ForEach-Object {
  @{path=$_.FullName.Substring($Build.Length+1).Replace('\','/');size=$_.Length;sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()}
 })
 $Manifest=@{format=1;backend='windows-native';os='windows';arch='amd64';version=$Version;commit=$Commit;agent_protocol=2;components=@{node=$NodeVersion;qodercli='1.1.32';qoderclicn='1.1.32';undici='6.29.1';sharp='0.35.5'};files=$Files}
 [IO.File]::WriteAllText((Join-Path $Build 'native-manifest.json'),($Manifest|ConvertTo-Json -Depth 8),[Text.UTF8Encoding]::new($false))
 # Explicit allowlist: the Node download cache and source/config/data never enter the runtime archive.
 Add-Type -AssemblyName System.IO.Compression
 Add-Type -AssemblyName System.IO.Compression.FileSystem
 $ZipPath=Join-Path $OutputDirectory 'cli2api-app_windows_amd64.zip'
 if(Test-Path -LiteralPath $ZipPath){throw 'Output package already exists; use a new output directory.'}
 $Zip=[IO.Compression.ZipFile]::Open($ZipPath,[IO.Compression.ZipArchiveMode]::Create)
 try {foreach($Entry in @($Files.path)+@('native-manifest.json')){[IO.Compression.ZipFileExtensions]::CreateEntryFromFile($Zip,(Join-Path $Build $Entry),$Entry,[IO.Compression.CompressionLevel]::Optimal)|Out-Null}}finally{$Zip.Dispose()}
 $Hash=(Get-FileHash -LiteralPath $ZipPath -Algorithm SHA256).Hash.ToLowerInvariant()
 $Sums=Join-Path $OutputDirectory 'cli2api-app_checksums.txt'
 [IO.File]::WriteAllText($Sums,"$Hash  cli2api-app_windows_amd64.zip`n",[Text.UTF8Encoding]::new($false))
 & (Join-Path $Components 'updater.exe') --unpack-native-package $ZipPath --package-checksums $Sums --target-version $Version --extract-to (Join-Path $Build 'verified-package')
 if($LASTEXITCODE -ne 0){throw 'runtime package verification failed'}
 Write-Output "Built and verified Windows amd64 package: $ZipPath"
}finally{$env:PATH=$OriginalPath;$env:GOOS=$OriginalGoOS;$env:GOARCH=$OriginalGoArch;$env:CGO_ENABLED=$OriginalCGO}
