$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Path C:\sysbox -Force | Out-Null
Start-Transcript -Path C:\sysbox\bootstrap.log -Append | Out-Null
try {
    $answer = Get-Volume -FileSystemLabel 'SYSCFG' | Select-Object -First 1
    if (-not $answer) { throw 'SYSCFG answer disc is missing' }
    Copy-Item "$(($answer.DriveLetter)):\finish.ps1" C:\sysbox\finish.ps1 -Force
    Copy-Item "$(($answer.DriveLetter)):\set-flag.ps1" C:\sysbox\set-flag.ps1 -Force

    $virtio = Get-PSDrive -PSProvider FileSystem | Where-Object {
        Test-Path "$($_.Root)guest-agent\qemu-ga-x86_64.msi"
    } | Select-Object -First 1
    if (-not $virtio) { throw 'VirtIO guest agent disc is missing' }
    $serialDriver = "$($virtio.Root)vioserial\2k25\amd64\vioser.inf"
    if (-not (Test-Path $serialDriver)) { throw 'Windows Server 2025 amd64 VirtIO serial driver is missing' }
    & pnputil.exe /add-driver $serialDriver /install | Out-File C:\sysbox\pnputil.log
    if ($LASTEXITCODE -ne 0) { throw "VirtIO serial driver installation failed: $LASTEXITCODE" }
    $msi = "$($virtio.Root)guest-agent\qemu-ga-x86_64.msi"
    $install = Start-Process msiexec.exe -ArgumentList @('/i', "`"$msi`"", '/qn', '/norestart') -Wait -PassThru
    if ($install.ExitCode -notin @(0, 3010)) { throw "QEMU guest agent installation failed: $($install.ExitCode)" }
    Set-Service -Name QEMU-GA -StartupType Automatic

    & schtasks.exe /Create /TN SysboxADFinish /TR 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\sysbox\finish.ps1' /SC ONSTART /RU SYSTEM /F | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not register AD setup task' }
    Set-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' AutoAdminLogon '0'
    Remove-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' DefaultPassword -ErrorAction SilentlyContinue
    Install-WindowsFeature AD-Domain-Services -IncludeManagementTools | Out-Null
    $password = ConvertTo-SecureString '__DSRM_PASSWORD__' -AsPlainText -Force
    Install-ADDSForest -DomainName 'arena.test' -DomainNetbiosName 'ARENA' -InstallDns `
        -SafeModeAdministratorPassword $password -NoRebootOnCompletion -Force
    Restart-Computer -Force
} catch {
    $_ | Out-File C:\sysbox\bootstrap-error.txt
    throw
} finally {
    Stop-Transcript | Out-Null
}
