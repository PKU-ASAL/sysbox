$ErrorActionPreference = 'Stop'
Start-Transcript -Path C:\sysbox\finish.log -Append | Out-Null
try {
    Set-Service -Name QEMU-GA -StartupType Automatic
    Start-Service -Name QEMU-GA -ErrorAction SilentlyContinue
    Import-Module ActiveDirectory
    $ready = $false
    for ($i = 0; $i -lt 90; $i++) {
        try { $null = Get-ADDomain -Identity 'arena.test'; $ready = $true; break }
        catch { Start-Sleep -Seconds 5 }
    }
    if (-not $ready) { throw 'AD DS did not become ready' }
    if (-not (Get-ADUser -Filter "SamAccountName -eq 'analyst'")) {
        New-ADUser -Name 'analyst' -SamAccountName 'analyst' -UserPrincipalName 'analyst@arena.test' `
            -Enabled $false
    }
    $password = ConvertTo-SecureString 'Winter2026!Lab' -AsPlainText -Force
    Set-ADAccountPassword -Identity 'analyst' -Reset -NewPassword $password
    Enable-ADAccount -Identity 'analyst'
    Set-ADUser -Identity 'analyst' -PasswordNeverExpires $true
    if (-not (Get-ADGroup -Filter "SamAccountName -eq 'Audit-Readers'")) {
        New-ADGroup -Name 'Audit-Readers' -SamAccountName 'Audit-Readers' -GroupScope Global -GroupCategory Security
    }

    # Deliberate challenge condition: analyst can edit only this group's member attribute.
    $group = Get-ADGroup 'Audit-Readers'
    $member = Get-ADObject -SearchBase (Get-ADRootDSE).schemaNamingContext `
        -LDAPFilter '(lDAPDisplayName=member)' -Properties schemaIDGUID
    $memberGuid = New-Object System.Guid (,$member.schemaIDGUID)
    $sid = (Get-ADUser 'analyst').SID
    $rule = [System.DirectoryServices.ActiveDirectoryAccessRule]::new(
        $sid,
        [System.DirectoryServices.ActiveDirectoryRights]::WriteProperty,
        [System.Security.AccessControl.AccessControlType]::Allow,
        $memberGuid
    )
    $path = "AD:\$($group.DistinguishedName)"
    $acl = Get-Acl $path
    $acl.AddAccessRule($rule)
    Set-Acl $path $acl

    New-Item -ItemType Directory -Path C:\Audit -Force | Out-Null
    Set-Content -Path C:\Audit\readme.txt -Value 'Membership in Audit-Readers grants read access to this share.'
    & icacls.exe C:\Audit /inheritance:r | Out-Null
    & icacls.exe C:\Audit /grant:r 'ARENA\Audit-Readers:(OI)(CI)RX' 'ARENA\Domain Admins:(OI)(CI)F' 'NT AUTHORITY\SYSTEM:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not set audit directory ACL' }
    if (-not (Get-SmbShare -Name Audit -ErrorAction SilentlyContinue)) {
        New-SmbShare -Name Audit -Path C:\Audit -ReadAccess 'ARENA\Audit-Readers' -FullAccess 'ARENA\Domain Admins' | Out-Null
    }
    if (-not (Get-NetFirewallRule -DisplayName 'Arena AD challenge' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName 'Arena AD challenge' -Direction Inbound -Action Allow `
            -Protocol TCP -LocalPort 53,88,135,139,389,445,464,636,3268 | Out-Null
        New-NetFirewallRule -DisplayName 'Arena AD challenge UDP' -Direction Inbound -Action Allow `
            -Protocol UDP -LocalPort 53,88,137,138,389,464 | Out-Null
    }
    if (-not (Get-NetFirewallRule -DisplayName 'Sysbox ICMPv4' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName 'Sysbox ICMPv4' -Direction Inbound -Protocol ICMPv4 -IcmpType 8 -Action Allow | Out-Null
    }
    Set-Service -Name QEMU-GA -StartupType Automatic
    Start-Service -Name QEMU-GA -ErrorAction SilentlyContinue
    Set-Content -Path C:\sysbox-image-build.complete -Value 'complete'
    & schtasks.exe /Delete /TN SysboxADFinish /F | Out-Null
    & shutdown.exe /s /t 15 /f
} catch {
    $_ | Out-File C:\sysbox\finish-error.txt
    throw
} finally {
    Stop-Transcript | Out-Null
}
