param(
    [Parameter(Mandatory=$true)][string]$HelperPath,
    [Parameter(Mandatory=$true)][string]$HelperSHA256,
    [Parameter(Mandatory=$true)][string]$IssuerPublicKeyBase64,
    [Parameter(Mandatory=$true)][string]$TopologyUID,
    [Parameter(Mandatory=$true)][string]$ResourceUID,
    [Parameter(Mandatory=$true)][long]$Generation
)
$ErrorActionPreference = 'Stop'
$root = 'C:\ProgramData\Sysbox\guest-task'
$worker = 'SysboxTaskWorker'
if ($Generation -lt 1 -or !$TopologyUID -or !$ResourceUID) { throw 'Complete node scope required' }
if ([Convert]::FromBase64String($IssuerPublicKeyBase64).Length -ne 32) { throw 'Ed25519 public key required' }
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $HelperPath).Hash.ToLowerInvariant() -ne $HelperSHA256.ToLowerInvariant()) { throw 'Helper SHA256 mismatch' }
# Replacement of a populated journal is never an installation side effect.
if (Test-Path "$root\config.json") { throw 'Helper already configured; drain and review upgrade separately' }
if (Get-LocalUser -Name $worker -ErrorAction SilentlyContinue) { throw 'Existing worker account is not owned by this installation' }
function Set-PrivateACL([string]$Path, [bool]$Directory) {
    if ($Directory) { $acl = New-Object System.Security.AccessControl.DirectorySecurity }
    else { $acl = New-Object System.Security.AccessControl.FileSecurity }
    $acl.SetAccessRuleProtection($true, $false)
    $system = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18')
    $admins = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-32-544')
    $acl.SetOwner($system)
    foreach ($sid in @($system, $admins)) {
        if ($Directory) {
            $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        } else { $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'Allow') }
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}
$parent = Split-Path $root
if (!(Test-Path $parent)) { New-Item -ItemType Directory -Path $parent | Out-Null; Set-PrivateACL $parent $true }
New-Item -ItemType Directory -Force -Path $root | Out-Null
Set-PrivateACL $root $true
New-Item -ItemType Directory -Path "$root\journal" | Out-Null
Set-PrivateACL "$root\journal" $true
Copy-Item -LiteralPath $HelperPath -Destination "$root\sysbox-windows-task.exe"
Set-PrivateACL "$root\sysbox-windows-task.exe" $false
$bytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes); $rng.Dispose()
$password = [Convert]::ToBase64String($bytes) + 'aA1!'
$secure = ConvertTo-SecureString $password -AsPlainText -Force
New-LocalUser -Name $worker -Password $secure -Description 'Sysbox durable task restricted worker' -PasswordNeverExpires -UserMayNotChangePassword | Out-Null
$users = Get-LocalGroup -SID 'S-1-5-32-545'
Add-LocalGroupMember -Group $users -Member $worker
$config = @{
    root = "$root\journal"
    public_key = $IssuerPublicKeyBase64
    scope = @{ topology_uid=$TopologyUID; resource_uid=$ResourceUID; generation=$Generation }
    worker_user = $worker
    worker_domain = '.'
    worker_password = $password
}
$utf8 = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText("$root\config.json", ($config | ConvertTo-Json -Depth 5 -Compress), $utf8)
Set-PrivateACL "$root\config.json" $false
$password = $null; $secure = $null; $config.worker_password = $null
Write-Output 'Sysbox Windows task helper installed with private journal and non-administrator worker'
