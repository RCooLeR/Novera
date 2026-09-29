package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	compileSDK      = "37"
	platformVersion = "37.0"
	buildTools      = "37.0.0"
	ndkVersion      = "29.0.14206865"
)

func main() {
	fmt.Println("Checking Android development dependencies...")
	fmt.Println()

	errors := []string{}

	// Check Go
	if !checkCommand("go", "version") {
		errors = append(errors, "Go is not installed. Install from https://go.dev/dl/")
	} else {
		fmt.Println("✓ Go is installed")
	}

	// Check ANDROID_HOME
	androidHome := os.Getenv("ANDROID_HOME")
	if androidHome == "" {
		androidHome = os.Getenv("ANDROID_SDK_ROOT")
	}
	if androidHome == "" {
		// Try common default locations
		home, _ := os.UserHomeDir()
		possiblePaths := []string{
			filepath.Join(home, "AppData", "Local", "Android", "Sdk"),
			filepath.Join(home, "Android", "Sdk"),
			filepath.Join(home, "Library", "Android", "sdk"),
			"/usr/local/share/android-sdk",
		}
		for _, p := range possiblePaths {
			if _, err := os.Stat(p); err == nil {
				androidHome = p
				break
			}
		}
	}

	if androidHome == "" {
		errors = append(errors, "ANDROID_HOME not set. Install Android Studio and set ANDROID_HOME environment variable")
	} else {
		fmt.Printf("✓ ANDROID_HOME: %s\n", androidHome)
		platformDir := filepath.Join(androidHome, "platforms", "android-"+platformVersion)
		if _, err := os.Stat(platformDir); err != nil {
			errors = append(errors, "Android SDK Platform "+compileSDK+" is not installed")
		}
		buildToolsDir := filepath.Join(androidHome, "build-tools", buildTools)
		if _, err := os.Stat(buildToolsDir); err != nil {
			errors = append(errors, "Android SDK Build-Tools "+buildTools+" is not installed")
		}
	}

	// Check adb
	if !checkCommand("adb", "version") {
		if androidHome != "" {
			platformTools := filepath.Join(androidHome, "platform-tools")
			errors = append(errors, fmt.Sprintf("adb not found. Add %s to PATH", platformTools))
		} else {
			errors = append(errors, "adb not found. Install Android SDK Platform-Tools")
		}
	} else {
		fmt.Println("✓ adb is installed")
	}

	// Check emulator
	if !checkCommand("emulator", "-list-avds") {
		if androidHome != "" {
			emulatorPath := filepath.Join(androidHome, "emulator")
			errors = append(errors, fmt.Sprintf("emulator not found. Add %s to PATH", emulatorPath))
		} else {
			errors = append(errors, "emulator not found. Install Android Emulator via SDK Manager")
		}
	} else {
		fmt.Println("✓ Android Emulator is installed")
	}

	// Check NDK
	ndkHome := os.Getenv("ANDROID_NDK_HOME")
	if ndkHome == "" && androidHome != "" {
		ndkHome = filepath.Join(androidHome, "ndk", ndkVersion)
	}

	if !hasNDKVersion(ndkHome, ndkVersion) {
		errors = append(errors, "Android NDK "+ndkVersion+" not found. Install that exact side-by-side version")
	} else {
		fmt.Printf("✓ Android NDK: %s\n", ndkHome)
	}

	// Check Java
	if major, ok := javaMajorVersion(); !ok || major < 17 {
		errors = append(errors, "JDK 17 or newer is required by Android Gradle Plugin 9.4")
	} else {
		fmt.Printf("✓ Java %d is installed\n", major)
	}

	// Check for AVD (Android Virtual Device)
	if checkCommand("emulator", "-list-avds") {
		cmd := exec.Command("emulator", "-list-avds")
		output, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			avds := strings.Split(strings.TrimSpace(string(output)), "\n")
			fmt.Printf("✓ Found %d Android Virtual Device(s)\n", len(avds))
		} else {
			fmt.Println("⚠ No Android Virtual Devices found. Create one via Android Studio > Tools > Device Manager")
		}
	}

	fmt.Println()

	if len(errors) > 0 {
		fmt.Println("❌ Missing dependencies:")
		for _, err := range errors {
			fmt.Printf("   - %s\n", err)
		}
		fmt.Println()
		fmt.Println("Setup instructions:")
		fmt.Println("1. Install Android Studio: https://developer.android.com/studio")
		fmt.Println("2. Open SDK Manager and install:")
		fmt.Println("   - Android SDK Platform (API " + compileSDK + ")")
		fmt.Println("   - Android SDK Build-Tools " + buildTools)
		fmt.Println("   - Android SDK Platform-Tools")
		fmt.Println("   - Android Emulator")
		fmt.Println("   - NDK (Side by side) " + ndkVersion)
		fmt.Println("3. Set environment variables:")
		switch runtime.GOOS {
		case "windows":
			fmt.Println(`   # PowerShell`)
			fmt.Println(`   $env:ANDROID_HOME = "$env:LOCALAPPDATA\Android\Sdk"`)
			fmt.Println(`   $env:Path += ";$env:ANDROID_HOME\platform-tools;$env:ANDROID_HOME\emulator"`)
		case "darwin":
			fmt.Println("   export ANDROID_HOME=$HOME/Library/Android/sdk")
			fmt.Println("   export PATH=$PATH:$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator")
		default:
			fmt.Println("   export ANDROID_HOME=$HOME/Android/Sdk")
			fmt.Println("   export PATH=$PATH:$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator")
		}
		fmt.Println("4. Create an AVD via Android Studio > Tools > Device Manager")
		os.Exit(1)
	}

	fmt.Println("✓ All Android development dependencies are installed!")
}

func hasNDKVersion(path, want string) bool {
	data, err := os.ReadFile(filepath.Join(path, "source.properties"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "Pkg.Revision = "+want)
}

func javaMajorVersion() (int, bool) {
	output, err := exec.Command("java", "-version").CombinedOutput()
	if err != nil {
		return 0, false
	}
	versionLine := string(output)
	const marker = "version \""
	start := strings.Index(versionLine, marker)
	if start < 0 {
		return 0, false
	}
	versionLine = versionLine[start+len(marker):]
	end := strings.IndexByte(versionLine, '"')
	if end < 0 {
		return 0, false
	}
	major, err := strconv.Atoi(strings.SplitN(versionLine[:end], ".", 2)[0])
	return major, err == nil
}

func checkCommand(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}
