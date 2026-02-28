package models

type InventoryReport struct {
	CollectedAt   string          `json:"collectedAt"`
	Source        string          `json:"source"`
	Hardware      HardwareInfo    `json:"hardware"`
	OS            OperatingSystem `json:"os"`
	LoggedInUsers []LoggedInUser  `json:"loggedInUsers"`
	Battery       []BatteryInfo   `json:"battery"`
	BitLocker     []BitLockerInfo `json:"bitLocker"`
	CPUInfo       []CPUInfo       `json:"cpuInfo"`
	CPUFeatures   []CPUFeature    `json:"cpuFeatures"`
	MemoryModules []MemoryModule  `json:"memoryModules"`
	Monitors      []MonitorInfo   `json:"monitors"`
	GPUs          []GPUInfo       `json:"gpus"`
	Volumes       []DiskInfo      `json:"volumes"`
	PhysicalDisks []DiskInfo      `json:"physicalDisks"`
	Disks         []DiskInfo      `json:"disks"`
	Networks      []NetworkInfo   `json:"networks"`
	Software      []SoftwareItem  `json:"software"`
	StartupItems  []StartupItem   `json:"startupItems"`
	Autoexec      []AutoexecItem  `json:"autoexec"`
}

type LoggedInUser struct {
	User     string `json:"user"`
	Type     string `json:"type"`
	TTY      string `json:"tty"`
	Host     string `json:"host"`
	PID      int    `json:"pid"`
	SID      string `json:"sid"`
	Registry string `json:"registry"`
	Time     int64  `json:"time"`
}

type HardwareInfo struct {
	Hostname     string  `json:"hostname"`
	Manufacturer string  `json:"manufacturer"`
	Model        string  `json:"model"`
	CPU          string  `json:"cpu"`
	LogicalCores int     `json:"logicalCores"`
	Cores        int     `json:"cores"`
	MemoryGB     float64 `json:"memoryGB"`

	MotherboardManufacturer string `json:"motherboardManufacturer"`
	MotherboardModel        string `json:"motherboardModel"`
	MotherboardSerial       string `json:"motherboardSerial"`
	BIOSVendor              string `json:"biosVendor"`
	BIOSVersion             string `json:"biosVersion"`
	BIOSReleaseDate         string `json:"biosReleaseDate"`
	BIOSSerial              string `json:"biosSerial"`
	MemoryModulesCount      int    `json:"memoryModulesCount"`
}

type MemoryModule struct {
	Handle              string  `json:"handle"`
	ArrayHandle         string  `json:"arrayHandle"`
	FormFactor          string  `json:"formFactor"`
	TotalWidth          int     `json:"totalWidth"`
	DataWidth           int     `json:"dataWidth"`
	SizeMB              int     `json:"sizeMB"`
	Set                 int     `json:"set"`
	Slot                string  `json:"slot"`
	Bank                string  `json:"bank"`
	MemoryTypeDetails   string  `json:"memoryTypeDetails"`
	MaxSpeedMTs         int     `json:"maxSpeedMTs"`
	Manufacturer        string  `json:"manufacturer"`
	PartNumber          string  `json:"partNumber"`
	Serial              string  `json:"serial"`
	AssetTag            string  `json:"assetTag"`
	SizeGB              float64 `json:"sizeGB"`
	SpeedMHz            int     `json:"speedMHz"`
	Type                string  `json:"type"`
	MinVoltageMV        int     `json:"minVoltageMV"`
	MaxVoltageMV        int     `json:"maxVoltageMV"`
	ConfiguredVoltageMV int     `json:"configuredVoltageMV"`
}

type BatteryInfo struct {
	Manufacturer         string `json:"manufacturer"`
	Model                string `json:"model"`
	SerialNumber         string `json:"serialNumber"`
	CycleCount           int    `json:"cycleCount"`
	State                string `json:"state"`
	Charging             bool   `json:"charging"`
	Charged              bool   `json:"charged"`
	DesignedCapacityMAh  int    `json:"designedCapacityMAh"`
	MaxCapacityMAh       int    `json:"maxCapacityMAh"`
	CurrentCapacityMAh   int    `json:"currentCapacityMAh"`
	PercentRemaining     int    `json:"percentRemaining"`
	AmperageMA           int    `json:"amperageMA"`
	VoltageMV            int    `json:"voltageMV"`
	MinutesUntilEmpty    int    `json:"minutesUntilEmpty"`
	MinutesToFullCharge  int    `json:"minutesToFullCharge"`
	Chemistry            string `json:"chemistry"`
	Health               string `json:"health"`
	Condition            string `json:"condition"`
	ManufactureDateEpoch int64  `json:"manufactureDateEpoch"`
}

type BitLockerInfo struct {
	DeviceID            string `json:"deviceId"`
	DriveLetter         string `json:"driveLetter"`
	PersistentVolumeID  string `json:"persistentVolumeId"`
	ConversionStatus    int    `json:"conversionStatus"`
	ProtectionStatus    int    `json:"protectionStatus"`
	EncryptionMethod    string `json:"encryptionMethod"`
	Version             int    `json:"version"`
	PercentageEncrypted int    `json:"percentageEncrypted"`
	LockStatus          int    `json:"lockStatus"`
}

type CPUInfo struct {
	DeviceID                 string `json:"deviceId"`
	Model                    string `json:"model"`
	Manufacturer             string `json:"manufacturer"`
	ProcessorType            string `json:"processorType"`
	CPUStatus                int    `json:"cpuStatus"`
	NumberOfCores            int    `json:"numberOfCores"`
	LogicalProcessors        int    `json:"logicalProcessors"`
	AddressWidth             int    `json:"addressWidth"`
	CurrentClockSpeed        int    `json:"currentClockSpeed"`
	MaxClockSpeed            int    `json:"maxClockSpeed"`
	SocketDesignation        string `json:"socketDesignation"`
	Availability             string `json:"availability"`
	LoadPercentage           int    `json:"loadPercentage"`
	NumberOfEfficiencyCores  int    `json:"numberOfEfficiencyCores"`
	NumberOfPerformanceCores int    `json:"numberOfPerformanceCores"`
}

type CPUFeature struct {
	Feature        string `json:"feature"`
	Value          string `json:"value"`
	OutputRegister string `json:"outputRegister"`
	OutputBit      int    `json:"outputBit"`
	InputEAX       string `json:"inputEAX"`
}

type MonitorInfo struct {
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Serial       string `json:"serial"`
	Resolution   string `json:"resolution"`
	Status       string `json:"status"`
}

type GPUInfo struct {
	Name          string  `json:"name"`
	Manufacturer  string  `json:"manufacturer"`
	DriverVersion string  `json:"driverVersion"`
	VRAMGB        float64 `json:"vramGB"`
	Status        string  `json:"status"`
}

type OperatingSystem struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Build        string `json:"build"`
	Architecture string `json:"architecture"`
}

type SoftwareItem struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	InstallID string `json:"installId"`
	Serial    string `json:"serial"`
	Source    string `json:"source"`
}

type StartupItem struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Args     string `json:"args"`
	Type     string `json:"type"`
	Source   string `json:"source"`
	Status   string `json:"status"`
	Username string `json:"username"`
}

type AutoexecItem struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

type DiskInfo struct {
	Device        string  `json:"device"`
	Label         string  `json:"label"`
	FileSystem    string  `json:"fileSystem"`
	Type          string  `json:"type"`
	SizeGB        float64 `json:"sizeGB"`
	FreeGB        float64 `json:"freeGB"`
	FreeKnown     bool    `json:"freeKnown"`
	BootPartition bool    `json:"bootPartition"`

	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Serial       string `json:"serial"`
	Partitions   int    `json:"partitions"`
	Description  string `json:"description"`
}

type NetworkInfo struct {
	Interface        string `json:"interface"`
	FriendlyName     string `json:"friendlyName"`
	MAC              string `json:"mac"`
	IPv4             string `json:"ipv4"`
	IPv6             string `json:"ipv6"`
	Gateway          string `json:"gateway"`
	Type             string `json:"type"`
	MTU              int    `json:"mtu"`
	LinkSpeedMbps    int    `json:"linkSpeedMbps"`
	ConnectionStatus string `json:"connectionStatus"`
	Enabled          bool   `json:"enabled"`
	PhysicalAdapter  bool   `json:"physicalAdapter"`
	DHCPEnabled      bool   `json:"dhcpEnabled"`
	DNSServers       string `json:"dnsServers"`
	Description      string `json:"description"`
	Manufacturer     string `json:"manufacturer"`
}
