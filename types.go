package dep2p

import (
	"fmt"
	"net"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/showwin/speedtest-go/speedtest"
	"github.com/sirupsen/logrus"
)

// 节点信息
type NodeInfo struct {
	HostNmae    string      // 主机名字
	OSName      string      // 主机系统
	DiskInfo    DiskInfo    // 硬盘信息
	NetworkInfo NetworkInfo // 网络信息
	CPUInfo     CPUInfo     // CPU信息
	MemoryInfo  MemoryInfo  // 内存信息
}

// 内存信息
type MemoryInfo struct {
	TotalMemory     string  // 内存总大小
	UsedMemory      string  // 已用大小
	AvailableMemory string  // 剩余大小
	MemoryUsage     float64 // 内存使用率
}

// CPU信息
type CPUInfo struct {
	CpuModel map[string]int32 // CPU信息
	CpuUsage float64          // 内存使用率
}

// 硬盘信息
type DiskInfo struct {
	TotalSize           string  // 硬盘大小
	FreeSpace           string  // 剩余空间
	UsedSpace           string  // 已使用空间
	UsagePercent        float64 // 使用率
	HomeDirTotalSize    string  // 当前用户硬盘大小
	HomeDirFreeSpace    string  // 当前用户剩余空间
	HomeDirUsedSpace    string  // 当前用户已使用空间
	HomeDirUsagePercent float64 // 当前用户使用率
}

// 网络信息
type NetworkInfo struct {
	IspName             string  // 网络类型
	UpstreamBandwidth   float64 // 上行带宽
	DownstreamBandwidth float64 // 下行带宽
	NetworkStatus       string  // 网络状态
	LocalIPAddress      string  // 本地IP地址
	PublicAddress       string  // 公网IP地址
	MACAddress          string  // MAC地址
	Lat                 string  // 纬度
	Lon                 string  // 经度
}

func GetNodeInfo() *NodeInfo {

	info, err := host.Info()
	if err != nil {
		logrus.Errorf("获取主机信息失败: %v", err)
		return nil
	}

	diskInfo, err := getDiskInfo()
	if err != nil {
		logrus.Errorf("获取硬盘信息失败: %v", err)
		return nil
	}

	// networkInfo, err := GetNetworkInfo()
	// if err != nil {
	// 	logrus.Errorf("获取网络信息失败: %v", err)
	// 	return nil
	// }

	cpuInfo, err := GetCPUInfo()
	if err != nil {
		logrus.Errorf("获取cpu信息失败: %v", err)
		return nil
	}

	memoryInfo, err := GetMemoryInfo()
	if err != nil {
		logrus.Errorf("获取内存信息失败: %v", err)
		return nil
	}

	nodeInfo := &NodeInfo{
		HostNmae: info.Hostname,
		OSName:   info.OS,
		DiskInfo: *diskInfo,
		//NetworkInfo: *networkInfo,
		CPUInfo:    *cpuInfo,
		MemoryInfo: *memoryInfo,
	}

	return nodeInfo
}

// 获取cpu信息
func GetCPUInfo() (*CPUInfo, error) {
	// 获取CPU信息
	cpuInfo, err := cpu.Info()
	if err != nil {
		fmt.Println("Failed to get CPU info:", err)
		return nil, err
	}

	// 创建一个map来记录每个型号的数量
	cpuModelCount := make(map[string]int32)

	for _, info := range cpuInfo {
		modelName := info.ModelName
		cpuModelCount[modelName] = cpuModelCount[modelName] + info.Cores
	}

	// 打印每个型号的数量
	// for modelName, count := range cpuModelCount {
	// 	fmt.Printf("CPU Model: %s, Count: %d\n", modelName, count)
	// }

	// 获取所有CPU核心的使用率
	// 设置采样时间间隔为1秒钟
	interval := 1 * time.Second
	cpuPercentages, err := cpu.Percent(interval, true)
	if err != nil {
		fmt.Println("Failed to get CPU usage:", err)
		return nil, err
	}

	// 计算总的使用率
	totalUsage := 0.0
	for _, percentage := range cpuPercentages {
		totalUsage += percentage
	}
	averageUsage := totalUsage / float64(len(cpuPercentages))

	cPUInfo := &CPUInfo{
		CpuModel: cpuModelCount, // CPU信息
		CpuUsage: averageUsage,  // 内存使用率
	}

	return cPUInfo, nil
}

// 获取内存信息
func GetMemoryInfo() (*MemoryInfo, error) {

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	// total := memInfo.Total
	// used := memInfo.Used
	// available := memInfo.Available
	// usedPercent := memInfo.UsedPercent

	// fmt.Printf("Total Memory: %.2f ", float64(total)/(1024*1024*1024))
	// fmt.Printf("Used Memory: %.2f ", float64(used)/(1024*1024*1024))
	// fmt.Printf("Available Memory: %.2f ", float64(available)/(1024*1024*1024))
	// fmt.Printf("Memory Usage: %.2f%%\n", usedPercent)

	totalMemoryStr := formatFileSize(memInfo.Total)
	usedMemoryStr := formatFileSize(memInfo.Used)
	availableMemoryStr := formatFileSize(memInfo.Available)

	memoryInfo := &MemoryInfo{
		TotalMemory:     totalMemoryStr,      // 硬盘信息
		UsedMemory:      usedMemoryStr,       // 网络信息
		AvailableMemory: availableMemoryStr,  // CPU信息
		MemoryUsage:     memInfo.UsedPercent, // 内存信息
	}

	return memoryInfo, nil
}

// 获取网络信
func GetNetworkInfo() (*NetworkInfo, error) {

	// ip地址
	ipAddress, err := getLocalIPAddress()
	if err != nil {
		fmt.Println("获取本机IP地址失败：", err)
		return nil, err
	}

	// mac地址
	macAddress, err := getLocalMACAddress()
	if err != nil {
		fmt.Println("获取本机MAC地址失败：", err)
		return nil, err
	}

	var speedtestClient = speedtest.New()

	user, err := speedtestClient.FetchUserInfo()
	if err != nil {
		fmt.Println("获取本机MAC地址失败：", err)
		return nil, err
	}

	serverList, err := speedtestClient.FetchServers()
	if err != nil {
		fmt.Println("获取本机MAC地址失败：", err)
		return nil, err
	}

	targets, err := serverList.FindServer([]int{})
	if err != nil {
		fmt.Println("获取本机MAC地址失败：", err)
		return nil, err
	}

	var upstreamBandwidth, downstreamBandwidth float64
	for _, s := range targets {
		// 请确保您的主机可以访问该测试服务器、
		// 否则会出现错误。
		// 建议此时更换服务器
		s.PingTest(nil)
		s.DownloadTest()
		s.UploadTest()

		upstreamBandwidth = s.ULSpeed
		downstreamBandwidth = s.DLSpeed
	}

	networkInfo := &NetworkInfo{
		IspName:             user.Isp,            // 网络类型
		UpstreamBandwidth:   upstreamBandwidth,   // 上行带宽
		DownstreamBandwidth: downstreamBandwidth, // 下行带宽
		NetworkStatus:       "Connected",         // 网络状态
		LocalIPAddress:      ipAddress,           // 本地IP地址
		PublicAddress:       user.IP,             // 公网IP地址
		MACAddress:          macAddress,          // MAC地址
		Lat:                 user.Lat,            // 纬度
		Lon:                 user.Lon,            // 经度
	}
	speedtestClient.Manager.Reset()
	return networkInfo, nil
}

func getLocalIPAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}

	return "", fmt.Errorf("无法获取本机IP地址")
}

func getLocalMACAddress() (string, error) {
	out, err := exec.Command("ifconfig").Output()
	if err != nil {
		return "", err
	}

	output := string(out)
	startIndex := strings.Index(output, "ether")
	if startIndex == -1 {
		return "", fmt.Errorf("无法解析MAC地址")
	}

	substr := output[startIndex+6:]
	endIndex := strings.Index(substr, " ")
	if endIndex == -1 {
		return "", fmt.Errorf("无法解析MAC地址")
	}

	macAddress := substr[:endIndex]

	return macAddress, nil
}

func getDiskInfo() (*DiskInfo, error) {
	diskInfo := new(DiskInfo)
	var err error
	switch os := runtime.GOOS; os {
	case "windows":
		diskInfo, err = getDiskUsageWindows()
	case "linux":
		diskInfo, err = getDiskUsageLinux()
	case "darwin":
		diskInfo, err = getDiskUsageMac()
	default:
		fmt.Println("Unsupported operating system")
	}

	return diskInfo, err
}

// 兼容mac系统获取硬盘大小
func getDiskUsageMac() (*DiskInfo, error) {

	partitions, err := disk.Partitions(false)
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	var totalSize, usedSpace, freeSpace uint64

	for _, partition := range partitions {
		// fmt.Println(partition.Mountpoint)
		// fmt.Println(partition.Mountpoint)
		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			fmt.Println("Error:", err)
			return nil, err
		}

		totalSize = usage.Total
		usedSpace = usage.Used
		freeSpace = usage.Free

	}

	totalSizeStr := formatFileSize(totalSize)
	usedSpaceStr := formatFileSize(usedSpace)
	freeSpaceStr := formatFileSize(freeSpace)
	usagePercent := float64(usedSpace) / float64(totalSize) * 100

	// fmt.Printf("Total Size: %s ", totalSizeStr)         // 硬盘大小
	// fmt.Printf("Used Space: %s ", usedSpaceStr)         // 已使用空间
	// fmt.Printf("Free Space: %s ", freeSpaceStr)         // 剩余空间
	// fmt.Printf("Usage Percent: %.2f%%\n", usagePercent) // 使用率

	// 获取当前用户目录
	currentUser, err := user.Current()
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	homeDir := currentUser.HomeDir

	// 计算指定目录的空间使用情况
	diskUsage, err := disk.Usage(homeDir)
	if err != nil {
		fmt.Printf("Error getting disk usage for %s: %s\n", homeDir, err)
		return nil, err
	}

	homeDirTotalSizeStr := formatFileSize(diskUsage.Total) // 当前用户硬盘大小
	homeDirUsedSpaceStr := formatFileSize(diskUsage.Used)  // 当前用户已使用空间
	homeDirFreeSpaceStr := formatFileSize(diskUsage.Free)  // 当前用户剩余空间
	homeDiUsagePercent := diskUsage.UsedPercent            // 当前用户使用率

	// 返回结构体
	diskInfo := &DiskInfo{
		TotalSize:           totalSizeStr,        // 硬盘大小
		FreeSpace:           freeSpaceStr,        // 剩余空间
		UsedSpace:           usedSpaceStr,        // 已使用空间
		UsagePercent:        usagePercent,        // 使用率
		HomeDirTotalSize:    homeDirTotalSizeStr, // 当前用户硬盘大小
		HomeDirFreeSpace:    homeDirUsedSpaceStr, // 当前用户剩余空间
		HomeDirUsedSpace:    homeDirFreeSpaceStr, // 当前用户已使用空间
		HomeDirUsagePercent: homeDiUsagePercent,  // 当前用户使用率
	}

	return diskInfo, nil

}

// 格式化
func formatFileSize(size uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.2f MB", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.2f KB", float64(size)/KB)
	default:
		return strconv.FormatUint(size, 10) + " B"
	}
}

// 兼容windows系统获取硬盘大小
func getDiskUsageWindows() (*DiskInfo, error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	var totalSize, usedSpace, freeSpace uint64

	for _, partition := range partitions {
		diskUsage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			fmt.Printf("Error getting disk usage for %s: %s\n", partition.Mountpoint, err)
			continue
		}

		totalSize += diskUsage.Total
		usedSpace += diskUsage.Used
		freeSpace += diskUsage.Free

		// fmt.Printf("Partition: %s\n", partition.Device)
		// fmt.Printf("Total Size: %.2f ", float64(diskUsage.Total)/(1024*1024*1024))
		// fmt.Printf("Used Space: %.2f ", float64(diskUsage.Used)/(1024*1024*1024))
		// fmt.Printf("Available Space: %.2f ", float64(diskUsage.Free)/(1024*1024*1024))
		// fmt.Printf("Usage Percent: %.2f%%\n", diskUsage.UsedPercent)
		// fmt.Println()
	}

	totalSizeStr := formatFileSize(totalSize)
	usedSpaceStr := formatFileSize(usedSpace)
	freeSpaceStr := formatFileSize(freeSpace)
	usagePercent := float64(usedSpace) / float64(totalSize) * 100

	currentUser, err := user.Current()
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	homeDir := currentUser.HomeDir

	diskUsage, err := disk.Usage(homeDir)
	if err != nil {
		fmt.Printf("Error getting disk usage for %s: %s\n", homeDir, err)
		return nil, err
	}

	homeDirTotalSizeStr := formatFileSize(diskUsage.Total) // 当前用户硬盘大小
	homeDirUsedSpaceStr := formatFileSize(diskUsage.Used)  // 当前用户已使用空间
	homeDirFreeSpaceStr := formatFileSize(diskUsage.Free)  // 当前用户剩余空间
	homeDiUsagePercent := diskUsage.UsedPercent            // 当前用户使用率

	// 返回结构体
	diskInfo := &DiskInfo{
		TotalSize:           totalSizeStr,        // 硬盘大小
		FreeSpace:           freeSpaceStr,        // 剩余空间
		UsedSpace:           usedSpaceStr,        // 已使用空间
		UsagePercent:        usagePercent,        // 使用率
		HomeDirTotalSize:    homeDirTotalSizeStr, // 当前用户硬盘大小
		HomeDirFreeSpace:    homeDirUsedSpaceStr, // 当前用户剩余空间
		HomeDirUsedSpace:    homeDirFreeSpaceStr, // 当前用户已使用空间
		HomeDirUsagePercent: homeDiUsagePercent,  // 当前用户使用率
	}

	return diskInfo, nil
}

// 兼容linux系统获取硬盘大小
func getDiskUsageLinux() (*DiskInfo, error) {

	partitions, err := disk.Partitions(false)
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	var totalSize, usedSpace, freeSpace uint64

	for _, partition := range partitions {

		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			fmt.Println("Error:", err)
			return nil, err
		}

		totalSize = usage.Total
		usedSpace = usage.Used
		freeSpace = usage.Free

	}

	totalSizeStr := formatFileSize(totalSize)
	usedSpaceStr := formatFileSize(usedSpace)
	freeSpaceStr := formatFileSize(freeSpace)
	usagePercent := float64(usedSpace) / float64(totalSize) * 100

	// fmt.Printf("Total Size: %s ", totalSizeStr)         // 硬盘大小
	// fmt.Printf("Used Space: %s ", usedSpaceStr)         // 已使用空间
	// fmt.Printf("Free Space: %s ", freeSpaceStr)         // 剩余空间
	// fmt.Printf("Usage Percent: %.2f%%\n", usagePercent) // 使用率

	// 获取当前用户目录
	currentUser, err := user.Current()
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}

	homeDir := currentUser.HomeDir

	// 计算指定目录的空间使用情况
	diskUsage, err := disk.Usage(homeDir)
	if err != nil {
		fmt.Printf("Error getting disk usage for %s: %s\n", homeDir, err)
		return nil, err
	}

	homeDirTotalSizeStr := formatFileSize(diskUsage.Total) // 当前用户硬盘大小
	homeDirUsedSpaceStr := formatFileSize(diskUsage.Used)  // 当前用户已使用空间
	homeDirFreeSpaceStr := formatFileSize(diskUsage.Free)  // 当前用户剩余空间
	homeDiUsagePercent := diskUsage.UsedPercent            // 当前用户使用率

	// 返回结构体
	diskInfo := &DiskInfo{
		TotalSize:           totalSizeStr,        // 硬盘大小
		FreeSpace:           freeSpaceStr,        // 剩余空间
		UsedSpace:           usedSpaceStr,        // 已使用空间
		UsagePercent:        usagePercent,        // 使用率
		HomeDirTotalSize:    homeDirTotalSizeStr, // 当前用户硬盘大小
		HomeDirFreeSpace:    homeDirUsedSpaceStr, // 当前用户剩余空间
		HomeDirUsedSpace:    homeDirFreeSpaceStr, // 当前用户已使用空间
		HomeDirUsagePercent: homeDiUsagePercent,  // 当前用户使用率
	}

	return diskInfo, nil
}
