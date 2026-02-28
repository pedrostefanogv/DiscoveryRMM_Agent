package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"winget-store/internal/models"
	"winget-store/internal/processutil"
)

// windowsHardwareDetails holds supplemental hardware data collected via
// PowerShell/CIM that osquery may not provide (monitors, BIOS serial, etc.).
type windowsHardwareDetails struct {
	MotherboardManufacturer string                `json:"motherboardManufacturer"`
	MotherboardModel        string                `json:"motherboardModel"`
	MotherboardSerial       string                `json:"motherboardSerial"`
	BIOSVendor              string                `json:"biosVendor"`
	BIOSVersion             string                `json:"biosVersion"`
	BIOSReleaseDate         string                `json:"biosReleaseDate"`
	BIOSSerial              string                `json:"biosSerial"`
	MemoryModules           []models.MemoryModule `json:"memoryModules"`
	Monitors                []models.MonitorInfo  `json:"monitors"`
	GPUs                    []models.GPUInfo      `json:"gpus"`
}

// collectWindowsHardwareDetails runs a PowerShell script that gathers
// motherboard, BIOS, memory, monitor and GPU details via Win32 CIM classes.
func collectWindowsHardwareDetails(ctx context.Context) (windowsHardwareDetails, error) {
	script := `$ErrorActionPreference = 'Stop'
$mb = Get-CimInstance Win32_BaseBoard | Select-Object -First 1
$bios = Get-CimInstance Win32_BIOS | Select-Object -First 1
$ram = Get-CimInstance Win32_PhysicalMemory | Where-Object { $_.Capacity -gt 0 } | ForEach-Object {
  [PSCustomObject]@{
    slot = $_.DeviceLocator
    bank = $_.BankLabel
    manufacturer = $_.Manufacturer
    partNumber = $_.PartNumber
    serial = $_.SerialNumber
    sizeGB = [math]::Round(($_.Capacity/1GB),2)
    speedMHz = [int]$_.Speed
    type = [string]$_.SMBIOSMemoryType
  }
}
$gpus = Get-CimInstance Win32_VideoController -ErrorAction SilentlyContinue | ForEach-Object {
	[PSCustomObject]@{
		name = $_.Name
		manufacturer = $_.AdapterCompatibility
		driverVersion = $_.DriverVersion
		vramGB = if($_.AdapterRAM -gt 0){ [math]::Round(($_.AdapterRAM/1GB),2) } else { 0 }
		status = $_.Status
	}
}
$mons = @()
try {
  $mons = Get-CimInstance -Namespace root\wmi -ClassName WmiMonitorID -ErrorAction Stop | ForEach-Object {
    $name = ([System.Text.Encoding]::ASCII.GetString($_.UserFriendlyName)).Trim([char]0)
    $man = ([System.Text.Encoding]::ASCII.GetString($_.ManufacturerName)).Trim([char]0)
    $ser = ([System.Text.Encoding]::ASCII.GetString($_.SerialNumberID)).Trim([char]0)
    [PSCustomObject]@{
      name = $name
      manufacturer = $man
      serial = $ser
      resolution = ''
      status = 'connected'
    }
  }
} catch {
  $mons = @()
}
$result = [PSCustomObject]@{
  motherboardManufacturer = $mb.Manufacturer
  motherboardModel = $mb.Product
  motherboardSerial = $mb.SerialNumber
	biosVendor = $bios.Manufacturer
	biosVersion = $bios.SMBIOSBIOSVersion
	biosReleaseDate = [string]$bios.ReleaseDate
	biosSerial = $bios.SerialNumber
	memoryModules = @($ram)
	monitors = @($mons)
	gpus = @($gpus)
}
$result | ConvertTo-Json -Depth 6 -Compress`

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return windowsHardwareDetails{}, fmt.Errorf("erro no detalhe de hardware windows: %w | saida: %s", err, strings.TrimSpace(string(output)))
	}

	var details windowsHardwareDetails
	if err := json.Unmarshal(output, &details); err != nil {
		return windowsHardwareDetails{}, fmt.Errorf("erro ao parsear detalhe de hardware windows: %w", err)
	}

	return details, nil
}

// collectWithPowerShell is the full PowerShell-based inventory fallback.
func collectWithPowerShell(ctx context.Context) (models.InventoryReport, error) {
	script := `$ErrorActionPreference = 'Stop'
$cs = Get-CimInstance Win32_ComputerSystem
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$os = Get-CimInstance Win32_OperatingSystem
$logical = Get-CimInstance Win32_LogicalDisk | Select-Object @{Name='device';Expression={$_.DeviceID}}, @{Name='label';Expression={$_.Description}}, @{Name='fileSystem';Expression={$_.FileSystem}}, @{Name='type';Expression={$_.DriveType}}, @{Name='sizeGB';Expression={if($_.Size -gt 0){[math]::Round(($_.Size/1GB),2)}else{0}}}, @{Name='freeGB';Expression={if($_.FreeSpace -ge 0){[math]::Round(($_.FreeSpace/1GB),2)}else{-1}}}, @{Name='freeKnown';Expression={$_.FreeSpace -ge 0}}, @{Name='bootPartition';Expression={$_.DeviceID -eq $os.SystemDrive}}, @{Name='description';Expression={$_.Description}}, @{Name='partitions';Expression={0}}, @{Name='manufacturer';Expression={''}}, @{Name='model';Expression={''}}, @{Name='serial';Expression={''}}
$physical = Get-CimInstance Win32_DiskDrive | Select-Object @{Name='device';Expression={"Disk$($_.Index)"}}, @{Name='label';Expression={$_.Caption}}, @{Name='fileSystem';Expression={''}}, @{Name='type';Expression={$_.InterfaceType}}, @{Name='sizeGB';Expression={[math]::Round(($_.Size/1GB),2)}}, @{Name='freeGB';Expression={-1}}, @{Name='freeKnown';Expression={$false}}, @{Name='bootPartition';Expression={$false}}, @{Name='manufacturer';Expression={$_.Manufacturer}}, @{Name='model';Expression={$_.Model}}, @{Name='serial';Expression={$_.SerialNumber}}, @{Name='partitions';Expression={[int]$_.Partitions}}, @{Name='description';Expression={$_.Description}}
$net = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | ForEach-Object {
	$cfg = Get-NetIPConfiguration -InterfaceIndex $_.ifIndex -ErrorAction SilentlyContinue
	[PSCustomObject]@{
		interface = $_.Name
		friendlyName = $_.InterfaceDescription
		mac = $_.MacAddress
		ipv4 = (($cfg.IPv4Address | ForEach-Object { $_.IPAddress }) -join ', ')
		ipv6 = (($cfg.IPv6Address | ForEach-Object { $_.IPAddress }) -join ', ')
		gateway = ((@($cfg.IPv4DefaultGateway.NextHop) + @($cfg.IPv6DefaultGateway.NextHop) | Where-Object { $_ }) -join ', ')
		type = $_.InterfaceType
		mtu = 0
		linkSpeedMbps = [int]($_.LinkSpeed / 1MB)
		connectionStatus = $_.Status.ToString()
		enabled = $_.Status -ne 'Disabled'
		physicalAdapter = $_.HardwareInterface
		dhcpEnabled = $cfg.NetAdapter.DhcpEnabled
		dnsServers = (($cfg.DNSServer.ServerAddresses) -join ', ')
		description = $_.InterfaceDescription
		manufacturer = ''
	}
}
$sw = Get-ItemProperty HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*, HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\* -ErrorAction SilentlyContinue |
      Where-Object { $_.DisplayName } |
			Select-Object @{Name='name';Expression={$_.DisplayName}}, @{Name='version';Expression={$_.DisplayVersion}}, @{Name='publisher';Expression={$_.Publisher}}, @{Name='installId';Expression={$_.PSChildName}}, @{Name='serial';Expression={($_.ProductID, $_.BundleUpgradeCode | Where-Object { $_ } | Select-Object -First 1)}}
$startup = Get-CimInstance Win32_StartupCommand -ErrorAction SilentlyContinue |
			Select-Object @{Name='name';Expression={$_.Name}}, @{Name='path';Expression={$_.Command}}, @{Name='args';Expression={$_.User}}, @{Name='type';Expression={'startup'}}, @{Name='source';Expression={$_.Location}}, @{Name='status';Expression={'enabled'}}, @{Name='username';Expression={$_.User}}
$autoexec = @()
$autoexec += (Get-CimInstance Win32_Service -ErrorAction SilentlyContinue | Where-Object { $_.PathName } | Select-Object @{Name='path';Expression={$_.PathName}}, @{Name='name';Expression={$_.Name}}, @{Name='source';Expression={'services'}})
$autoexec += (Get-ScheduledTask -ErrorAction SilentlyContinue | ForEach-Object {
		$taskName = $_.TaskName
		foreach ($a in $_.Actions) {
			if ($a.Execute) {
				[PSCustomObject]@{ path = ($a.Execute + ' ' + $a.Arguments).Trim(); name = $taskName; source = 'scheduled_tasks' }
			}
		}
	})
$autoexec += ($startup | Select-Object @{Name='path';Expression={$_.path}}, @{Name='name';Expression={$_.name}}, @{Name='source';Expression={'startup_items'}})
$autoexec = $autoexec | Where-Object { $_.path -or $_.name }
$loggedUsers = @()
try {
  $sessions = Get-CimInstance Win32_LogonSession -ErrorAction Stop | Where-Object { $_.LogonType -eq 2 -or $_.LogonType -eq 10 -or $_.LogonType -eq 11 }
  foreach ($s in $sessions) {
    $assoc = Get-CimAssociatedInstance -InputObject $s -ResultClassName Win32_UserAccount -ErrorAction SilentlyContinue
    if ($assoc) {
      foreach ($u in $assoc) {
        $loggedUsers += [PSCustomObject]@{
          user = $u.Caption
          type = switch ([int]$s.LogonType) { 2 { 'interactive' } 10 { 'remote_interactive' } 11 { 'cached_interactive' } default { [string]$s.LogonType } }
          tty = ''
          host = ''
          pid = 0
          sid = $u.SID
          registry = ''
          time = 0
        }
      }
    }
  }
  $loggedUsers = $loggedUsers | Sort-Object user -Unique
} catch {
  $loggedUsers = @([PSCustomObject]@{ user = $env:USERNAME; type = 'interactive'; tty = ''; host = $env:COMPUTERNAME; pid = 0; sid = ''; registry = ''; time = 0 })
}
$result = [PSCustomObject]@{
  collectedAt = [DateTime]::UtcNow.ToString('o')
  hardware = [PSCustomObject]@{
    hostname = $env:COMPUTERNAME
    manufacturer = $cs.Manufacturer
    model = $cs.Model
    cpu = $cpu.Name
		logicalCores = [int]$cpu.NumberOfLogicalProcessors
    cores = [int]$cpu.NumberOfCores
    memoryGB = [math]::Round(($cs.TotalPhysicalMemory / 1GB), 2)
  }
  os = [PSCustomObject]@{
    name = $os.Caption
    version = $os.Version
    build = $os.BuildNumber
    architecture = $os.OSArchitecture
  }
	volumes = $logical
	physicalDisks = $physical
	disks = $logical
	networks = $net
  software = $sw
	startupItems = $startup
	autoexec = $autoexec
	loggedInUsers = @($loggedUsers)
}
$result | ConvertTo-Json -Depth 6 -Compress`

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return models.InventoryReport{}, fmt.Errorf("erro no fallback powershell: %w | saida: %s", err, strings.TrimSpace(string(output)))
	}

	var raw struct {
		CollectedAt string `json:"collectedAt"`
		Hardware    struct {
			Hostname     string  `json:"hostname"`
			Manufacturer string  `json:"manufacturer"`
			Model        string  `json:"model"`
			CPU          string  `json:"cpu"`
			LogicalCores int     `json:"logicalCores"`
			Cores        int     `json:"cores"`
			MemoryGB     float64 `json:"memoryGB"`
		} `json:"hardware"`
		OS struct {
			Name         string `json:"name"`
			Version      string `json:"version"`
			Build        string `json:"build"`
			Architecture string `json:"architecture"`
		} `json:"os"`
		Volumes       []models.DiskInfo    `json:"volumes"`
		PhysicalDisks []models.DiskInfo    `json:"physicalDisks"`
		Disks         []models.DiskInfo    `json:"disks"`
		Networks      []models.NetworkInfo `json:"networks"`
		Software      []struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Publisher string `json:"publisher"`
			InstallID string `json:"installId"`
			Serial    string `json:"serial"`
		} `json:"software"`
		StartupItems  []models.StartupItem  `json:"startupItems"`
		Autoexec      []models.AutoexecItem `json:"autoexec"`
		LoggedInUsers []models.LoggedInUser `json:"loggedInUsers"`
	}

	if err := json.Unmarshal(output, &raw); err != nil {
		return models.InventoryReport{}, fmt.Errorf("erro ao parsear json do powershell: %w", err)
	}

	report := models.InventoryReport{
		CollectedAt: raw.CollectedAt,
		Source:      "powershell-fallback",
		Hardware: models.HardwareInfo{
			Hostname:     raw.Hardware.Hostname,
			Manufacturer: raw.Hardware.Manufacturer,
			Model:        raw.Hardware.Model,
			CPU:          raw.Hardware.CPU,
			LogicalCores: raw.Hardware.LogicalCores,
			Cores:        raw.Hardware.Cores,
			MemoryGB:     raw.Hardware.MemoryGB,
		},
		OS: models.OperatingSystem{
			Name:         raw.OS.Name,
			Version:      raw.OS.Version,
			Build:        raw.OS.Build,
			Architecture: raw.OS.Architecture,
		},
		Battery:       []models.BatteryInfo{},
		BitLocker:     []models.BitLockerInfo{},
		CPUInfo:       []models.CPUInfo{},
		CPUFeatures:   []models.CPUFeature{},
		MemoryModules: []models.MemoryModule{},
		Monitors:      []models.MonitorInfo{},
		GPUs:          []models.GPUInfo{},
		Volumes:       raw.Volumes,
		PhysicalDisks: raw.PhysicalDisks,
		Disks:         raw.Disks,
		Networks:      raw.Networks,
		Software:      make([]models.SoftwareItem, 0, len(raw.Software)),
		StartupItems:  raw.StartupItems,
		Autoexec:      raw.Autoexec,
		LoggedInUsers: raw.LoggedInUsers,
	}

	if len(report.Volumes) == 0 && len(report.Disks) > 0 {
		report.Volumes = report.Disks
	}
	if len(report.Disks) == 0 && len(report.Volumes) > 0 {
		report.Disks = report.Volumes
	}

	for _, sw := range raw.Software {
		report.Software = append(report.Software, models.SoftwareItem{
			Name:      sw.Name,
			Version:   sw.Version,
			Publisher: sw.Publisher,
			InstallID: sw.InstallID,
			Serial:    sw.Serial,
			Source:    "registry",
		})
	}

	if details, derr := collectWindowsHardwareDetails(ctx); derr == nil {
		report.Hardware.MotherboardManufacturer = details.MotherboardManufacturer
		report.Hardware.MotherboardModel = details.MotherboardModel
		report.Hardware.MotherboardSerial = details.MotherboardSerial
		report.Hardware.BIOSVendor = details.BIOSVendor
		report.Hardware.BIOSVersion = details.BIOSVersion
		report.Hardware.BIOSReleaseDate = details.BIOSReleaseDate
		report.Hardware.BIOSSerial = details.BIOSSerial
		report.MemoryModules = details.MemoryModules
		report.Monitors = details.Monitors
		report.GPUs = details.GPUs
	}
	report.Hardware.MemoryModulesCount = len(report.MemoryModules)
	sanitizeHardwareFields(&report)

	return report, nil
}
