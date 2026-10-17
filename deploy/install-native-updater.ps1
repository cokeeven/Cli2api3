[CmdletBinding()]
param(
 [Parameter(Mandatory=$true)][string]$InstallRoot,
 [Parameter(Mandatory=$true)][string]$DataDirectory,
 [Parameter(Mandatory=$true)][string]$LaunchConfig,
 [Parameter(Mandatory=$true)][string]$UpdaterBinary,
 [Parameter(Mandatory=$true)][string]$UpdaterChecksums,
 [string]$ProgramDirectory,
 [string]$UpdaterDirectory,
 [string]$TaskName='ali2api3-Server',
 [string]$UpdaterTaskName='ali2api3-NativeUpdater',
 [ValidateRange(1024,65535)][int]$Port=3011,
 [string]$HealthURL='http://127.0.0.1:3010/health',
 [string]$BootstrapPackage,
 [string]$PackageChecksums,
 [string]$Version,
 [switch]$IsolatedProcessMode
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
function Full([string]$p){[IO.Path]::GetFullPath($p)}
function Write-Json([string]$p,$v){[IO.File]::WriteAllText($p,($v|ConvertTo-Json -Depth 12),[Text.UTF8Encoding]::new($false))}
function Quote([string]$s){'"'+$s+'"'}
function Secure([string]$p,[string]$gatewayIdentity){
 $acl=Get-Acl -LiteralPath $p;$acl.SetAccessRuleProtection($true,$false)
 $inherit=[Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
 foreach($identity in @([Security.Principal.WindowsIdentity]::GetCurrent().Name,'SYSTEM')|Select-Object -Unique){$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($identity,'FullControl',$inherit,'None','Allow'))}
 if($gatewayIdentity -and $gatewayIdentity -ne [Security.Principal.WindowsIdentity]::GetCurrent().Name){$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($gatewayIdentity,'ReadAndExecute',$inherit,'None','Allow'))}
 Set-Acl -LiteralPath $p -AclObject $acl
}
function Read-Health {try{Invoke-RestMethod -Uri $HealthURL -TimeoutSec 2}catch{$null}}
function Managed-Process {
 $exe=Join-Path $ProgramDirectory 'qoder-api-proxy.exe'
 $all=@(Get-CimInstance Win32_Process|Where-Object {$_.ExecutablePath -and $_.ExecutablePath -ieq $exe})
 if($all.Count -gt 1){throw 'More than one gateway uses this program path; split installations before onboarding.'}
 if($all.Count){$all[0]}else{$null}
}
function Stop-Gateway {
 $process=Managed-Process
 if($process){
  # Capture only descendants of the verified gateway. CreationDate prevents PID reuse.
  $all=@(Get-CimInstance Win32_Process);$owned=@($process);$ids=@([uint32]$process.ProcessId)
  do {$new=@($all|Where-Object {$_.ParentProcessId -in $ids -and $_.ProcessId -notin $ids});$owned+= $new;$ids+=@($new|ForEach-Object ProcessId)}while($new.Count)
  if(!$IsolatedProcessMode){Stop-ScheduledTask -TaskName $TaskName}
  foreach($p in $owned){$now=Get-CimInstance Win32_Process -Filter "ProcessId=$($p.ProcessId)" -ErrorAction SilentlyContinue;if($now -and $now.CreationDate -eq $p.CreationDate -and $now.ExecutablePath -eq $p.ExecutablePath){Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop}}
  for($i=0;$i -lt 100;$i++){if(!(Managed-Process)){return};Start-Sleep -Milliseconds 100};throw 'Gateway did not exit; no files replaced.'
 }
}
function Start-Gateway {
 if($IsolatedProcessMode){Start-Process -FilePath $GatewayScript -ArgumentList ('--run-native-gateway '+(Quote $GatewayConfig)) -WindowStyle Hidden|Out-Null}
 else{Start-ScheduledTask -TaskName $TaskName}
}
function Start-Supervisor {
 if($IsolatedProcessMode){$script:SupervisorProcess=Start-Process -FilePath $SupervisorScript -ArgumentList ('--supervise-native '+(Quote $SupervisorConfig)) -WindowStyle Hidden -PassThru}
 else{Start-ScheduledTask -TaskName $UpdaterTaskName}
}
if(!$ProgramDirectory){$ProgramDirectory=Join-Path $InstallRoot 'program'}
if(!$UpdaterDirectory){$UpdaterDirectory=Join-Path $InstallRoot 'updater-native'}
$InstallRoot=Full $InstallRoot;$DataDirectory=Full $DataDirectory;$ProgramDirectory=Full $ProgramDirectory;$UpdaterDirectory=Full $UpdaterDirectory;$LaunchConfig=Full $LaunchConfig
foreach($p in @($ProgramDirectory,$DataDirectory,$UpdaterDirectory)){
 for($cursor=$p;$cursor;$cursor=[IO.Path]::GetDirectoryName($cursor)){if((Test-Path -LiteralPath $cursor) -and ((Get-Item -LiteralPath $cursor).Attributes -band [IO.FileAttributes]::ReparsePoint)){throw 'Linked installation paths are unsupported.'}}
}
foreach($a in @($ProgramDirectory,$DataDirectory,$UpdaterDirectory)){foreach($b in @($ProgramDirectory,$DataDirectory,$UpdaterDirectory)){if($a -ne $b -and $a.StartsWith($b.TrimEnd('\')+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'Program, data, and updater directories must be separate.'}}}
if((Test-Path -LiteralPath (Join-Path $ProgramDirectory 'components')) -and !(Test-Path -LiteralPath (Join-Path $ProgramDirectory 'native-manifest.json'))){throw 'Existing components directory has no managed manifest; do not overwrite unowned runtime/config files. Review the layout first.'}
$hash=[Security.Cryptography.SHA256]::Create();try{$mutexHash=[BitConverter]::ToString($hash.ComputeHash([Text.Encoding]::UTF8.GetBytes($ProgramDirectory.ToLowerInvariant()))).Replace('-','')}finally{$hash.Dispose()}
$installMutex=[Threading.Mutex]::new($false,('Global\Cli2api3Onboarding-'+$mutexHash));$installOwned=$false
try {
 try{$installOwned=$installMutex.WaitOne(0)}catch [Threading.AbandonedMutexException]{$installOwned=$true}
 if(!$installOwned){throw 'Another onboarding operation already owns this installation.'}
$principal=[Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if(!$IsolatedProcessMode -and !$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){throw 'Administrator rights are required to preserve/update the scheduled tasks. Open PowerShell using Run as administrator and rerun this exact command. No policy bypass is required.'}
$userLaunch=Get-Content -LiteralPath $LaunchConfig -Raw -Encoding UTF8|ConvertFrom-Json
foreach($key in @('working_directory','arguments','env_file','runtime_dir','environment')){if(!$userLaunch.PSObject.Properties[$key]){throw "LaunchConfig must explicitly supply $key; preserve the existing deployment values."}}
if(!(Test-Path -LiteralPath $userLaunch.working_directory -PathType Container)){throw 'Configured working directory does not exist.'}
if($userLaunch.env_file -and !(Test-Path -LiteralPath $userLaunch.env_file -PathType Leaf)){throw 'Configured environment file does not exist.'}
$nativeConfig=Join-Path $UpdaterDirectory 'native.json';$TokenFile=Join-Path $UpdaterDirectory 'token.txt'
$listen="127.0.0.1:$Port";$agentURL="http://$listen"
if(Test-Path -LiteralPath $nativeConfig){$saved=Get-Content -LiteralPath $nativeConfig -Raw -Encoding UTF8|ConvertFrom-Json;if($saved.program_dir -ine $ProgramDirectory -or $saved.data_dir -ine $DataDirectory -or $saved.target_name -ine $TaskName){throw 'Saved native configuration belongs to different paths/task. Review it explicitly; it was not overwritten.'}}
$connections=@(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)
if($connections.Count){
 if(!(Test-Path -LiteralPath $nativeConfig) -or !(Test-Path -LiteralPath $TokenFile)){throw 'Updater port is already occupied. Inspect Get-NetTCPConnection and Get-Process; choose another free port. No process was stopped.'}
 $previous=Get-Content -LiteralPath $nativeConfig -Raw -Encoding UTF8|ConvertFrom-Json
 if($previous.program_dir -ine $ProgramDirectory -or $previous.data_dir -ine $DataDirectory){throw 'Existing updater belongs to a different installation.'}
 $token=[IO.File]::ReadAllText($TokenFile).Trim()
 try{$status=Invoke-RestMethod "$agentURL/v1/status" -Headers @{Authorization="Bearer $token"} -TimeoutSec 3}catch{throw 'Port owner is not the expected authenticated updater. No process was stopped.'}
 if($status.backend -ne 'windows-native'){throw 'Port belongs to a different updater backend.'}
 $peer=@($connections|Where-Object LocalAddress -EQ '127.0.0.1');if($peer.Count -ne 1){throw 'Updater port ownership is ambiguous.'};$peerProcess=Get-CimInstance Win32_Process -Filter "ProcessId=$($peer[0].OwningProcess)";if(!$peerProcess.ExecutablePath -or !$peerProcess.ExecutablePath.StartsWith(($UpdaterDirectory.TrimEnd('\')+'\bin\'),[StringComparison]::OrdinalIgnoreCase)){throw 'Authenticated port is owned by another installation.'}
 Write-Output 'Native updater is already connected; token and running tasks retained.'
 return
}
if(Test-Path -LiteralPath (Join-Path $UpdaterDirectory 'transaction.json')){
 $journal=Get-Content -LiteralPath (Join-Path $UpdaterDirectory 'transaction.json') -Raw -Encoding UTF8|ConvertFrom-Json
 if($journal.phase -notin @('succeeded','rolled_back','aborted')){throw 'An interrupted transaction exists. Start the installed native supervisor to reconcile its journal before rerunning onboarding.'}
}
$binaryHash=(Get-FileHash -LiteralPath $UpdaterBinary -Algorithm SHA256).Hash.ToLowerInvariant()
$expected=@(Get-Content -LiteralPath $UpdaterChecksums|Where-Object {$_ -match '^([a-fA-F0-9]{64})\s+cli2api-updater_windows_amd64.exe$'})
if($expected.Count -ne 1 -or ($expected[0] -split '\s+')[0].ToLowerInvariant() -ne $binaryHash){throw 'Windows amd64 updater checksum mismatch.'}
if($TaskName -eq $UpdaterTaskName){throw 'Gateway and updater task names must be different.'}
if(!$IsolatedProcessMode){$existing=Get-ScheduledTask -TaskName $UpdaterTaskName -ErrorAction SilentlyContinue;if($existing -and (@($existing.Actions).Count -ne 1 -or !$existing.Actions[0].Arguments.Contains((Join-Path $UpdaterDirectory 'supervisor.json')))){throw 'Updater task name belongs to another action; choose a different updater task name.'}}
$originalTask=$null;$gatewayIdentity=''
if(!$IsolatedProcessMode){$originalTask=Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop;if(@($originalTask.Actions).Count -ne 1){throw 'Gateway task must have exactly one reviewed action.'};$gatewayIdentity=$originalTask.Principal.UserId}
[IO.Directory]::CreateDirectory($UpdaterDirectory)|Out-Null
Secure $UpdaterDirectory $gatewayIdentity
foreach($p in @($ProgramDirectory,$DataDirectory,(Join-Path $UpdaterDirectory 'logs'))){[IO.Directory]::CreateDirectory($p)|Out-Null}
if($gatewayIdentity){$logs=Join-Path $UpdaterDirectory 'logs';$acl=Get-Acl -LiteralPath $logs;$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($gatewayIdentity,'Modify',([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit),'None','Allow'));Set-Acl -LiteralPath $logs -AclObject $acl}
if(!(Test-Path -LiteralPath $TokenFile)){$bytes=New-Object byte[] 32;$rng=[Security.Cryptography.RandomNumberGenerator]::Create();try{$rng.GetBytes($bytes)}finally{$rng.Dispose()};[IO.File]::WriteAllText($TokenFile,([BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant()),[Text.UTF8Encoding]::new($false))}
$instance=if(Test-Path -LiteralPath $nativeConfig){(Get-Content -LiteralPath $nativeConfig -Raw -Encoding UTF8|ConvertFrom-Json).instance_id}else{[guid]::NewGuid().ToString('N')}
$GatewayScript=$null;$SupervisorScript=$null
$GatewayConfig=Join-Path $UpdaterDirectory 'gateway-launch.json';$SupervisorConfig=Join-Path $UpdaterDirectory 'supervisor.json'
$Network=Join-Path $UpdaterDirectory 'network.json';if(!(Test-Path -LiteralPath $Network)){Write-Json $Network @{api_mirrors=@();download_mirrors=@();image_mirrors=@();proxy_url='';request_timeout_seconds=8;download_timeout_seconds=120;pull_timeout_seconds=180}}
$seedDir=Join-Path $UpdaterDirectory ('bin\'+$binaryHash);[IO.Directory]::CreateDirectory($seedDir)|Out-Null;$seed=Join-Path $seedDir 'cli2api-updater.exe';if(!(Test-Path -LiteralPath $seed)){Copy-Item -LiteralPath $UpdaterBinary -Destination $seed};if((Get-FileHash -LiteralPath $seed -Algorithm SHA256).Hash.ToLowerInvariant() -ne $binaryHash){throw 'Existing seed binary checksum mismatch.'}
$GatewayScript=$seed;$SupervisorScript=$seed
$gateway=@{program_dir=$ProgramDirectory;data_dir=$DataDirectory;working_directory=$userLaunch.working_directory;arguments=@($userLaunch.arguments);env_file=$userLaunch.env_file;runtime_dir=$userLaunch.runtime_dir;environment=$userLaunch.environment;agent_url=$agentURL;token_file=$TokenFile;instance_id=$instance;network_file=$Network;log_file=(Join-Path $UpdaterDirectory 'logs\gateway')}
Write-Json $GatewayConfig $gateway
$manager=if($IsolatedProcessMode){'process'}else{'task'}
Write-Json $nativeConfig @{program_dir=$ProgramDirectory;data_dir=$DataDirectory;state_dir=$UpdaterDirectory;health_url=$HealthURL;instance_id=$instance;manager=$manager;target_name=$TaskName;launcher=$GatewayScript;launch_config=$GatewayConfig;health_timeout_seconds=120;stop_timeout_seconds=30}
Write-Json $SupervisorConfig @{program_dir=$ProgramDirectory;updater_dir=$UpdaterDirectory;native_config=$nativeConfig;instance_id=$instance;listen=$listen;token_file=$TokenFile;status_file=(Join-Path $UpdaterDirectory 'status.json');network_file=$Network;seed_binary=$seed;log_file=(Join-Path $UpdaterDirectory 'logs\updater')}
& $seed --backend windows-native --native-config $nativeConfig --auth-token-file $TokenFile --validate-native-config
if($LASTEXITCODE -ne 0){throw 'Native configuration validation failed; existing gateway was not stopped.'}
$id='bootstrap-'+[guid]::NewGuid().ToString('N');$stage=Join-Path $ProgramDirectory ".cli2api-update\stage\$id"
if($BootstrapPackage){
 if(!$PackageChecksums -or !$Version){throw 'BootstrapPackage requires PackageChecksums and Version.'}
 [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($stage))|Out-Null
 & $seed --unpack-native-package $BootstrapPackage --package-checksums $PackageChecksums --target-version $Version --extract-to $stage
 if($LASTEXITCODE -ne 0){throw 'Bootstrap package failed verification; existing gateway left running.'}
}
if(!$BootstrapPackage -and !(Test-Path -LiteralPath (Join-Path $ProgramDirectory 'native-manifest.json'))){throw 'Initial onboarding requires the full supported application BootstrapPackage, not only updater.exe.'}
$oldHealth=Read-Health
$oldFiles=@{};foreach($name in @('qoder-api-proxy.exe','components','native-manifest.json')){$oldFiles[$name]=Test-Path -LiteralPath (Join-Path $ProgramDirectory $name)}
if($originalTask){$xml=Join-Path $UpdaterDirectory 'original-gateway-task.xml';if(!(Test-Path -LiteralPath $xml)){[IO.File]::WriteAllText($xml,(Export-ScheduledTask -TaskName $TaskName),[Text.UTF8Encoding]::new($false))}}
if(Test-Path -LiteralPath (Join-Path $DataDirectory 'qoder.db')){
 & $seed --snapshot-database (Join-Path $DataDirectory 'qoder.db') --backup-directory (Join-Path $DataDirectory 'backups')
 if($LASTEXITCODE -ne 0){throw 'SQLite snapshot/manifest verification failed; existing gateway left running.'}
}
if(!$IsolatedProcessMode){
 $action=New-ScheduledTaskAction -Execute $GatewayScript -Argument ('--run-native-gateway '+(Quote $GatewayConfig)) -WorkingDirectory $userLaunch.working_directory
 Set-ScheduledTask -TaskName $TaskName -Action $action|Out-Null
}
$transaction=$null;$script:SupervisorProcess=$null;$supervisorStarted=$false
try{
 if($BootstrapPackage){
  $before=@{ok=$true;version='';commit='';instance_id='';pid=0};if($oldHealth){$before.version=$oldHealth.version;$before.commit=$oldHealth.commit}
  $transaction=@{id=$id;phase='stopping';request=@{current_version=$before.version;target_version=$Version;skip_pull=$true};before=$before;stage=$stage;old=$oldFiles}
  Write-Json (Join-Path $UpdaterDirectory 'transaction.json') $transaction
  Stop-Gateway
  $transaction.phase='replacing';Write-Json (Join-Path $UpdaterDirectory 'transaction.json') $transaction
  foreach($name in @('qoder-api-proxy.exe','components','native-manifest.json')){
   $target=Join-Path $ProgramDirectory $name;$backup=Join-Path $ProgramDirectory ".cli2api-update\rollback\$id\$name"
   if($oldFiles[$name]){[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($backup))|Out-Null;Move-Item -LiteralPath $target -Destination $backup}
   Move-Item -LiteralPath (Join-Path $stage $name) -Destination $target
  }
 }else{Stop-Gateway}
 Start-Gateway
 $healthy=$false
 for($i=0;$i -lt 120;$i++){
  $health=Read-Health;$p=Managed-Process
  if($health -and $p -and $health.ok -and $health.instance_id -eq $instance -and $health.pid -eq $p.ProcessId){
   $manifest=Get-Content -LiteralPath (Join-Path $ProgramDirectory 'native-manifest.json') -Raw -Encoding UTF8|ConvertFrom-Json
   if($health.version -eq $manifest.version -and $health.commit -eq $manifest.commit){$healthy=$true;break}
  };Start-Sleep -Seconds 1
 }
 if(!$healthy){throw 'Supported gateway did not pass instance/version/commit verification.'}
 if($transaction){$transaction.phase='succeeded';Write-Json (Join-Path $UpdaterDirectory 'transaction.json') $transaction}
 if(!$IsolatedProcessMode){
  $action=New-ScheduledTaskAction -Execute $SupervisorScript -Argument ('--supervise-native '+(Quote $SupervisorConfig)) -WorkingDirectory $UpdaterDirectory
  $trigger=New-ScheduledTaskTrigger -AtStartup
  $settings=New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
  Register-ScheduledTask -TaskName $UpdaterTaskName -Action $action -Trigger $trigger -Settings $settings -User 'SYSTEM' -RunLevel Highest -Force|Out-Null
 }
 Start-Supervisor
 $supervisorStarted=$true;$connected=$false
 $authToken=[IO.File]::ReadAllText($TokenFile).Trim()
 for($i=0;$i -lt 40;$i++){try{$agentStatus=Invoke-RestMethod "$agentURL/v1/status" -Headers @{Authorization="Bearer $authToken"} -TimeoutSec 2;if($agentStatus.available -and $agentStatus.backend -eq 'windows-native'){$connected=$true;break}}catch{};Start-Sleep -Milliseconds 500}
 if(!$connected){throw 'Native supervisor did not expose the expected authenticated loopback agent.'}
 Write-Output "Native updater installed at $agentURL. Data and user launch settings retained; later releases require confirmation in System settings."
}catch{
 $cause=$_.Exception.Message
 if($supervisorStarted){
  if($IsolatedProcessMode){if($script:SupervisorProcess -and !$script:SupervisorProcess.HasExited){Stop-Process -Id $script:SupervisorProcess.Id -Force}}else{Stop-ScheduledTask -TaskName $UpdaterTaskName}
  $ownedPrefix=$UpdaterDirectory.TrimEnd('\')+'\bin\'
  Get-CimInstance Win32_Process|Where-Object {$_.ExecutablePath -and $_.ExecutablePath.StartsWith($ownedPrefix,[StringComparison]::OrdinalIgnoreCase) -and $_.CommandLine -and $_.CommandLine.Contains('--native-config') -and $_.CommandLine.Contains($nativeConfig)}|ForEach-Object {Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue}
 }
 Stop-Gateway
 if($transaction){
  foreach($name in @('qoder-api-proxy.exe','components','native-manifest.json')){
   $target=Join-Path $ProgramDirectory $name;$backup=Join-Path $ProgramDirectory ".cli2api-update\rollback\$id\$name"
   if(Test-Path -LiteralPath $backup){if(Test-Path -LiteralPath $target){$failed=Join-Path $ProgramDirectory ".cli2api-update\failed\$id\$name";[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($failed))|Out-Null;Move-Item -LiteralPath $target -Destination $failed};Move-Item -LiteralPath $backup -Destination $target}
  };$transaction.phase='rolling_back';Write-Json (Join-Path $UpdaterDirectory 'transaction.json') $transaction
 }
 if($originalTask){Set-ScheduledTask -TaskName $TaskName -Action $originalTask.Actions|Out-Null}
 Start-Gateway
 if($transaction){
  $restored=$false
  if($oldFiles['qoder-api-proxy.exe'] -and $oldHealth){for($i=0;$i -lt 30;$i++){$h=Read-Health;$p=Managed-Process;if($h -and $p -and $h.ok -and $h.version -eq $oldHealth.version -and $h.commit -eq $oldHealth.commit){$owner=@(Get-NetTCPConnection -State Listen -LocalPort ([uri]$HealthURL).Port -ErrorAction SilentlyContinue|Where-Object OwningProcess -EQ $p.ProcessId);if($owner.Count){$restored=$true;break}};Start-Sleep -Milliseconds 500}}
  $transaction.phase=if($restored){'rolled_back'}else{'recovery_failed'};Write-Json (Join-Path $UpdaterDirectory 'transaction.json') $transaction
 }
 throw "Native onboarding failed: $cause. Previous program/action restored where available; latest database retained. Review transaction.json and logs before retrying."
}

} finally {if($installOwned){$installMutex.ReleaseMutex()};$installMutex.Dispose()}
