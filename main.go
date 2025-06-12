package main

import (
    "eDBG/cli"
	"eDBG/config"
	"flag"
	"os"
	"strconv"
	"syscall"
    "fmt"
    "os/signal"
	"strings"
    "eDBG/module"
	"eDBG/utils"
	"eDBG/controller"
	"encoding/json"
    "eDBG/event"
	"github.com/inconshreveable/go-update"
	"net/http"
    _ "github.com/shuLhan/go-bindata" // add for bindata in Makefile
)
type UserBreakPoints struct {
	LibName    string `json:"brklib"`
	Offset 	   uint64 `json:"offset"`
	Enable	   bool   `json:"enable"`
} 

var EdbgConfigVersion int

type AppConfig struct {
	ConfigVersion   int       `json:"ConfigVersion"`
    PackageName     string    `json:"packagename"`
    LibName 		string    `json:"libname"`
	Hidereg			bool 	  `json:"hidereg"`
	Hidedis			bool	  `json:"hidedis"`
	BreakPoints	[]UserBreakPoints `json:"brk"`
	TNames      []string      `json:"tname"`
}

func doUpdate(url string) error {
	// 在 adb shell 里似乎无法联网（悲
	resp, err := http.Get(url)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    err = update.Apply(resp.Body, update.Options{})
    if err != nil {
        return err
    }
    return nil
}
func Update(proxy bool) {
	fmt.Println("Updating...")
	var err error
	if proxy {
		err = doUpdate("https://gh-proxy.com/github.com/ShinoLeah/eDBG/releases/latest/download/eDBG_arm64/")
	} else {
		err = doUpdate("https://github.com/ShinoLeah/eDBG/releases/latest/download/eDBG_arm64/")
	}
	if err != nil {
		fmt.Printf("Update Failed: %v\n", err)
	} else {
		fmt.Println("eDBG is up-to-date.")
	}
	os.Exit(0)
}
func SaveConfig(path string, cfg AppConfig) error {
    jsonData, err := json.MarshalIndent(cfg, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, jsonData, 0644)
}

func LoadConfig(path string) (AppConfig, error) {
    var cfg AppConfig
    jsonData, err := os.ReadFile(path)
    if err != nil {
        return cfg, err
    }
    err = json.Unmarshal(jsonData, &cfg)
    return cfg, err
}

func main() {
	EdbgConfigVersion = 1
	stopper := make(chan os.Signal, 1)
    signal.Notify(stopper, os.Interrupt, syscall.SIGTERM)
	workedlib := make(map[string]*controller.LibraryInfo)
	var (
		inputfile string
		outputfile string
		packageName string
		libName string
        hiddis bool
        hidreg bool
		save  bool
		prefer string
		threadFilters string
		disableColor bool
		brkAddressInfos []*controller.Address
		doupdate bool
		proxy bool
		disablePkgChk bool
	)
	var brkFlag string
	doupdate = false
	proxy = false
	flag.StringVar(&brkFlag, "b", "", "Breakpoint addresses in hex format, e.g., [0x1234,0x5678]")
	flag.StringVar(&packageName, "p", "", "Target package name")
	flag.StringVar(&libName, "l", "", "Target library name")
	// 无法运行的功能，先放着
	// flag.BoolVar(&doupdate, "update", false, "Update eDBG")
	// flag.BoolVar(&proxy, "update-proxy", false, "Update eDBG With Proxy")
    flag.BoolVar(&hidreg, "hide-register", false, "Hide Register Window")
	flag.BoolVar(&disablePkgChk, "disable-package-check", false, "Don't check package name")
    flag.BoolVar(&hiddis, "hide-disassemble", false, "Hide Disassemble Window")
	flag.StringVar(&threadFilters, "t", "", "Thread name filters, e.g., [name1,name2]")
	flag.StringVar(&inputfile, "i", "", "Input file saved from edbg. e.g. sample.edbg")
	flag.BoolVar(&save, "s", false, "Save your progress to input file")
	flag.BoolVar(&disableColor, "disable-color", false, "Disable color display")
	flag.StringVar(&prefer, "prefer", "", "Preference. 'uprobe' for Uprobes and 'hardware' for Hardware breakpoints")
	flag.StringVar(&outputfile, "o", "&&NotSetNotSetNotSetO=O", "Save your progress to specified file")
	flag.Parse()
	config.DisablePackageCheck = disablePkgChk
	if doupdate {
		Update(false)
	}
	if proxy {
		Update(true)
	}

	if inputfile != "" {
		cfg, err := LoadConfig(inputfile)
		if err != nil {
			fmt.Printf("Failed to load input file: %v\n", err)
			os.Exit(1)
		}

		if cfg.ConfigVersion != EdbgConfigVersion {
			fmt.Printf("Config version %d is not supported now.\n", cfg.ConfigVersion)
			os.Exit(1)
		}

		packageName = cfg.PackageName
		libName = cfg.LibName
		hidreg = cfg.Hidereg
		hiddis = cfg.Hidedis

		fmt.Printf("Using Config from: %s\n", inputfile)
	}
	if packageName == "" {
		fmt.Println("No Package Specified. Use -p com.package.name")
		os.Exit(1)
	}
	if libName == "" {
		fmt.Println("No Library Specified. Use -l libraryname.so")
		os.Exit(1)
	}

	process, err := controller.CreateProcess(packageName)
	if err != nil {
		fmt.Println("Create process error: ", err)
		os.Exit(1)
	}

	library, err := controller.CreateLibrary(process, libName)
	if err != nil {
		fmt.Println("Create Library error: ", err)
		os.Exit(1)
	}
	workedlib[libName] = library
	btfFile := ""
	if !utils.CheckConfig("CONFIG_DEBUG_INFO_BTF=y") {
		btfFile = utils.FindBTFAssets()
	}
	switch prefer {
	case "":
		config.Preference = config.PREFER_PERF
	case "uprobe":
		config.Preference = config.ALL_UPROBE
	case "hardware":
		config.Preference = config.ALL_PERF
	default:
		fmt.Println("Unsupported preference. Usage: -prefer uprobe/hardware")
		os.Exit(1)
	}
	if disableColor {
		config.GREEN = ""
		config.YELLOW = ""
		config.RED = ""
		config.BLUE = ""
		config.CYAN = ""
		config.NC = ""
	}
	if !utils.CheckConfig("CONFIG_HAVE_HW_BREAKPOINT=y") {
		fmt.Println("Hardware breakpoints not enabled. Using uprobes.")
		config.Preference = config.ALL_UPROBE
		config.Available_HW = 0
	}
    eventListener := event.CreateEventListener(process)
    brkManager := module.CreateBreakPointManager(eventListener, btfFile, process)
    client := cli.CreateClient(process, library, brkManager, &cli.UserConfig{
        Registers: !hidreg,
        Disasm: !hiddis,
    })
    if inputfile == "" {
		brkAddrs, err := ParseBreakPoints(brkFlag)
		if err != nil {
			fmt.Println("Create Breakpoints Failed: ", err)
			os.Exit(1)
		}

		tNames, err := ParseThreadNames(threadFilters)
		if err != nil {
			fmt.Println("Create Thread names Failed: ", err)
			os.Exit(1)
		}

		for _, name := range tNames {
			client.AddThreadFilterName(name)
		}

		for _, offset := range brkAddrs {
			brkAddressInfos = append(brkAddressInfos, controller.NewAddress(library, offset))
		}
	}  else {
		Config, _ := LoadConfig(inputfile)
		for _, name := range Config.TNames {
			client.AddThreadFilterName(name)
		}
		for _, brk := range Config.BreakPoints {
			val, ok := workedlib[brk.LibName]
			var libInfo *controller.LibraryInfo
			if !ok {
				_libinfo, err := controller.CreateLibrary(process, brk.LibName)
				if err != nil {
					fmt.Printf("Cannot locate library %s: %v. Skipped.\n", brk.LibName, err)
					continue
				}
				libInfo = _libinfo
				workedlib[brk.LibName] = libInfo
			} else {
				libInfo = val
			}

			if brk.Enable {
				brkAddressInfos = append(brkAddressInfos, controller.NewAddress(libInfo, brk.Offset))
			} else {
				brkManager.CreateBreakPoint(controller.NewAddress(libInfo, brk.Offset), false)
			}
		}
	}
	
	eventListener.SetupClient(client)
    
	err = brkManager.Start(brkAddressInfos)
	if err != nil {
		fmt.Println("Module start Failed: ", err)
		fmt.Println("Possible reasons:\n\n1. Some instructions do not support uprobe. Try setting breakpoints on other instructions or use until to skip the current instruction.\n2. Breakpoints with invalid addresses exist. Check the breakpoint list.\n")
		os.Exit(1)
	}
	fmt.Printf("Working on %s in %s. Press Ctrl+C to quit\n", libName, packageName)

    client.Run()
	eventListener.Run()
    <-stopper
	fmt.Println("Quiting eDBG...")
    process.Continue()
	// _ = brkManager.Stop()
	if save {
		if inputfile == "" {
			fmt.Println("No input file!")
		} else {
			outputfile = inputfile
		}
	}
	if outputfile != "&&NotSetNotSetNotSetO=O" {
		cfg := AppConfig{
			ConfigVersion: EdbgConfigVersion,
			PackageName: packageName,
			LibName: libName,
			Hidereg: hidreg,
			Hidedis: hiddis,
		}

		for _, brk := range brkManager.BreakPoints {
			if brk.Deleted || brk.Hardware {
				continue
			}
			cfg.BreakPoints = append(cfg.BreakPoints, UserBreakPoints{
				LibName: brk.Addr.LibInfo.LibName,
				Offset: brk.Addr.Offset,
				Enable:  brk.Enable,
			})
		}

		for _, t := range client.Config.ThreadFilters {
			if !t.Enable {
				continue
			}
			tName := t.Thread.Name
			if tName != "" {
				cfg.TNames = append(cfg.TNames, tName)
			}
		}
		SaveConfig(outputfile, cfg)
		fmt.Println("Progress saved to file: ", outputfile)
	}
}


func ParseBreakPoints(brkFlag string) ([]uint64, error) {
	trimmed := strings.Trim(brkFlag, "[]")
	if trimmed == "" {
		return []uint64{}, nil
	}

	addresses := strings.Split(trimmed, ",")
	var brkAddrs []uint64
	for _, addrStr := range addresses {
		addrStr = strings.TrimSpace(addrStr)
		addr, err := strconv.ParseUint(addrStr, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("ParseBreakPoints: Invalid address %q: %v", addrStr, err)
		}
		brkAddrs = append(brkAddrs, addr)
	}
	return brkAddrs, nil
}

func ParseThreadNames(brkFlag string) ([]string, error) {
	trimmed := strings.Trim(brkFlag, "[]")
	if trimmed == "" {
		return []string{}, nil
	}
	addresses := strings.Split(trimmed, ",")
	var brkAddrs []string
	for _, addrStr := range addresses {
		addrStr = strings.TrimSpace(addrStr)
		brkAddrs = append(brkAddrs, addrStr)
	}
	return brkAddrs, nil
}