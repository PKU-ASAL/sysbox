$ErrorActionPreference = 'Stop'
Import-Module ActiveDirectory
if ($env:FLAG -notmatch '^[0-9a-f]{64}$') { throw 'FLAG must be 64 lowercase hex characters' }
if (-not (Get-ADGroup -Identity 'Audit-Readers' -ErrorAction SilentlyContinue)) { throw 'AD group is missing' }
if (-not (Get-SmbShare -Name Audit -ErrorAction SilentlyContinue)) { throw 'Audit SMB share is missing' }
[System.IO.File]::WriteAllText('C:\Audit\flag.txt', $env:FLAG + "`r`n", [System.Text.Encoding]::ASCII)
& icacls.exe C:\Audit\flag.txt /inheritance:r | Out-Null
& icacls.exe C:\Audit\flag.txt /grant:r 'ARENA\Audit-Readers:R' 'ARENA\Domain Admins:F' 'NT AUTHORITY\SYSTEM:F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not set flag ACL' }
