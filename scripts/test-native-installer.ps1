[CmdletBinding()]
param(
 [Parameter(Mandatory=$true)][string]$ApplicationDirectory,
 [Parameter(Mandatory=$true)][string]$WorkDirectory,
 [switch]$RequireTasks
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$Repo=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$WorkDirectory=[IO.Path]::GetFullPath($WorkDirectory)
[IO.Directory]::CreateDirectory($WorkDirectory)|Out-Null
$root=Join-Path $WorkDirectory ('isolated-'+[guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($root)|Out-Null
$taskName='Cli2api3-IsolatedInstaller-'+[guid]::NewGuid().ToString('N')
$updaterTask=$taskName+'-Updater';$unrelated=$taskName+'-Unrelated'
$admin=([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if($RequireTasks -and !$admin){throw 'Real task rehearsal requires an elevated Windows test runner; it must not be reported as passed from process-mode tests.'}
function Free-Port {$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0);$l.Start();$p=([Net.IPEndPoint]$l.LocalEndpoint).Port;$l.Stop();$p}
function Json([string]$p,$v){[IO.File]::WriteAllText($p,($v|ConvertTo-Json -Depth 8),[Text.UTF8Encoding]::new($false))}
$package=Join-Path $ApplicationDirectory 'cli2api-app_windows_amd64.zip';$sums=Join-Path $ApplicationDirectory 'cli2api-app_checksums.txt'
$expected=((Get-Content -LiteralPath $sums|Where-Object {$_ -match '\s+cli2api-app_windows_amd64.zip$'}) -split '\s+')[0]
if((Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected){throw 'Test application checksum mismatch'}
Add-Type -AssemblyName System.IO.Compression.FileSystem
$z=[IO.Compression.ZipFile]::OpenRead($package)
try{
 $reader=[IO.StreamReader]::new($z.GetEntry('native-manifest.json').Open());try{$manifest=$reader.ReadToEnd()|ConvertFrom-Json}finally{$reader.Dispose()}
 $seed=Join-Path $root 'seed.exe';[IO.Compression.ZipFileExtensions]::ExtractToFile($z.GetEntry('components/updater.exe'),$seed)
}finally{$z.Dispose()}
$seedRecord=@($manifest.files|Where-Object path -EQ 'components/updater.exe')[0]
$seedHash=(Get-FileHash -LiteralPath $seed -Algorithm SHA256).Hash.ToLowerInvariant();if($seedHash -ne $seedRecord.sha256){throw 'Seed updater checksum mismatch'}
$seedSums=Join-Path $root 'fixture-updater-checksums.txt';[IO.File]::WriteAllText($seedSums,"$seedHash  cli2api-updater_windows_amd64.exe`n")
$port=Free-Port;$gatewayPort=Free-Port;$healthURL="http://127.0.0.1:$gatewayPort/health"
$launch=Join-Path $root 'original-launch.json'
Json $launch @{working_directory=$root;arguments=@();env_file='';runtime_dir=(Join-Path $root 'runtime');environment=@{HOST='127.0.0.1';PORT=[string]$gatewayPort;QODER_HOME=(Join-Path $root 'fictional-home')}}
$parameters=@{InstallRoot=$root;DataDirectory=(Join-Path $root 'data');LaunchConfig=$launch;UpdaterBinary=$seed;UpdaterChecksums=$seedSums;TaskName=$taskName;UpdaterTaskName=$updaterTask;Port=$port;HealthURL=$healthURL;BootstrapPackage=$package;PackageChecksums=$sums;Version=$manifest.version}
if(!$RequireTasks){$parameters.IsolatedProcessMode=$true}
# The first bootstrap starts from a real, unconnected gateway with fictional persisted state.
$data=Join-Path $root 'data';$program=Join-Path $root 'program';[IO.Directory]::CreateDirectory($program)|Out-Null
Push-Location $Repo
try{
 & go run ./internal/updater/testdata/nativeseed -data $data
 if($LASTEXITCODE -ne 0){throw 'Fictional database seed failed'}
 & go build -ldflags '-X github.com/caigee-cmd/cli2api/internal/buildinfo.Version=v9.0.1 -X github.com/caigee-cmd/cli2api/internal/buildinfo.Commit=1111111111111111111111111111111111111111' -o (Join-Path $program 'qoder-api-proxy.exe') ./cmd/server
 if($LASTEXITCODE -ne 0){throw 'Initial gateway build failed'}
}finally{Pop-Location}
$fixtureHome=Join-Path $root 'fictional-home';[IO.Directory]::CreateDirectory($fixtureHome)|Out-Null
[IO.File]::WriteAllText((Join-Path $fixtureHome 'auth.json'),'{"fake":"do-not-replace"}')
$fixtureHomeHash=(Get-FileHash -LiteralPath (Join-Path $fixtureHome 'auth.json') -Algorithm SHA256).Hash
[IO.File]::WriteAllText((Join-Path $root '.env'),"HOST=127.0.0.1`nPORT=$gatewayPort`nQODER_DATA_DIR=$data`nQODER_HOME=$fixtureHome`nQODER_RUNTIME_DIR=$(Join-Path $root 'runtime')`n")
$sentinelXml='';$originalIdentity=''
try{
 if($RequireTasks){
  $action=New-ScheduledTaskAction -Execute (Join-Path $program 'qoder-api-proxy.exe') -WorkingDirectory $root
  Register-ScheduledTask -TaskName $taskName -Action $action -User SYSTEM -RunLevel Highest -Force|Out-Null
  Register-ScheduledTask -TaskName $unrelated -Action (New-ScheduledTaskAction -Execute 'cmd.exe' -Argument '/c exit 0') -User SYSTEM -RunLevel Highest -Force|Out-Null
  $sentinelXml=Export-ScheduledTask -TaskName $unrelated;$originalIdentity=(Get-ScheduledTask -TaskName $taskName).Principal.UserId
 }
 if($RequireTasks){Start-ScheduledTask -TaskName $taskName}else{Start-Process -FilePath (Join-Path $program 'qoder-api-proxy.exe') -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput (Join-Path $root 'before.out.log') -RedirectStandardError (Join-Path $root 'before.err.log')|Out-Null}
 $before=$null;for($i=0;$i -lt 60;$i++){try{$before=Invoke-RestMethod $healthURL -TimeoutSec 2;if($before.ok){break}}catch{};Start-Sleep -Milliseconds 500}
 if(!$before -or $before.version -ne 'v9.0.1'){throw 'Unconnected initial gateway did not start'}
 & (Join-Path $Repo 'deploy\install-native-updater.ps1') @parameters
 $tokenPath=Join-Path $root 'updater-native\token.txt';$token=[IO.File]::ReadAllText($tokenPath).Trim();$tokenHash=(Get-FileHash -LiteralPath $tokenPath -Algorithm SHA256).Hash
 $status=$null
 for($i=0;$i -lt 40;$i++){try{$status=Invoke-RestMethod "http://127.0.0.1:$port/v1/status" -Headers @{Authorization="Bearer $token"} -TimeoutSec 2;if($status.available){break}}catch{};Start-Sleep -Milliseconds 500}
 if(!$status -or $status.backend -ne 'windows-native'){throw 'First onboarding did not connect a real native agent'}
 $health=Invoke-RestMethod $healthURL -TimeoutSec 5;if($health.version -ne $manifest.version -or $health.commit -ne $manifest.commit){throw 'Onboarded gateway artifact differs from application package'}
 if((Get-FileHash -LiteralPath (Join-Path $fixtureHome 'auth.json') -Algorithm SHA256).Hash -ne $fixtureHomeHash){throw 'Fictional authentication HOME was replaced'}
 foreach($endpoint in @('/api/accounts','/api/keys')){$view=Invoke-RestMethod ("http://127.0.0.1:$gatewayPort"+$endpoint) -Headers @{Authorization='Bearer fictional-onboarding-console-key'} -TimeoutSec 5;if(@($view.data).Count -ne 1){throw 'Existing fictional account/key was not retained'}}
 $pidBefore=@(Get-NetTCPConnection -LocalPort $port -State Listen)[0].OwningProcess
 & (Join-Path $Repo 'deploy\install-native-updater.ps1') @parameters
 if((Get-FileHash -LiteralPath $tokenPath -Algorithm SHA256).Hash -ne $tokenHash){throw 'Repeat installation rotated the token'}
 if(@(Get-NetTCPConnection -LocalPort $port -State Listen)[0].OwningProcess -ne $pidBefore){throw 'Repeat installation started another updater'}
 $denied=$false;try{Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$port/v1/status" -TimeoutSec 3|Out-Null}catch{if([int]$_.Exception.Response.StatusCode -eq 401){$denied=$true}}
 if(!$denied){throw 'Agent accepted an unauthenticated request'}
 $listener=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0);$listener.Start();$conflictPort=([Net.IPEndPoint]$listener.LocalEndpoint).Port
 try{
  $conflict=@{};foreach($entry in $parameters.GetEnumerator()){$conflict[$entry.Key]=$entry.Value};$conflict.InstallRoot=Join-Path $root 'other';$conflict.DataDirectory=Join-Path $root 'other-data';$conflict.Port=$conflictPort
  $rejected=$false;try{& (Join-Path $Repo 'deploy\install-native-updater.ps1') @conflict}catch{if($_.Exception.Message -like '*port is already occupied*'){$rejected=$true}else{throw}}
  if(!$rejected -or !$listener.Server.IsBound){throw 'Port conflict did not preserve its existing owner'}
 }finally{$listener.Stop()}
 if($RequireTasks){if((Get-ScheduledTask -TaskName $taskName).Principal.UserId -ne $originalIdentity){throw 'Gateway task identity changed'};if((Export-ScheduledTask -TaskName $unrelated) -ne $sentinelXml){throw 'Unrelated task was modified'}}
 $result=@{first_onboarding=$true;existing_data_retained=$true;authentication_home_retained=$true;repeat_install=$true;token_retained=$true;single_agent=$true;authentication=$true;port_conflict_rejected=$true;real_tasks=[bool]$RequireTasks;version=$health.version;commit=$health.commit;isolated_root=$root}
 Json (Join-Path $WorkDirectory 'installer-results.json') $result
 $result|ConvertTo-Json
}finally{
 if($RequireTasks){foreach($task in @($updaterTask,$taskName,$unrelated)){if(Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue){Stop-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue;Unregister-ScheduledTask -TaskName $task -Confirm:$false}}}
 # The unique generated root is resolved above; stop only executables physically inside it.
 $prefix=$root.TrimEnd('\')+'\'
 Get-CimInstance Win32_Process|Where-Object {$_.ExecutablePath -and $_.ExecutablePath.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)}|ForEach-Object {Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue}
}
