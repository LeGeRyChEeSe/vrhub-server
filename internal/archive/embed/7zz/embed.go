package embed7zz

import (
	"embed"
)

//go:embed 7zz_* libc++_shared.so LICENSE.txt
var binaries embed.FS

// BinaryNames maps "os/arch" keys to the embedded filename.
var BinaryNames = map[string]string{
	"windows/amd64": "7zz_windows_amd64.exe",
	"windows/arm64": "7zz_windows_arm64.exe",
	"linux/amd64":   "7zz_linux_amd64",
	"linux/arm64":   "7zz_linux_arm64",
	"linux/android": "7zz_android_arm64",
	"darwin/amd64":  "7zz_darwin",
	"darwin/arm64":  "7zz_darwin",
}

// ExpectedSHA256 maps "os/arch" keys to the expected SHA-256 hex digest.
var ExpectedSHA256 = map[string]string{
	"windows/amd64": "5BEA5AF5215089D97749D438351D7288310D0B8A472616EDBE1B34168AD1001C",
	"windows/arm64": "1D81501A5289D8866FFF1B609496273244FA67E3D54C51CB4C9BAF5B2165A2A5",
	"linux/amd64":   "EEA104E8C832B1ED6C63875ED2F50BAB80A36DACF8DD0490811058CF20BB2FC5",
	"linux/arm64":   "31095CA4229B988B348DE572BCA2D119D196CC9EA20682BCB619CCCE74BDBB94",
	"linux/android": "761FB579F2613D685DA4F6667F9C31F494ECC6BBF7723FB849AFA888F8CF457D",
	"darwin/amd64":  "4D1BAEAA33A40E7D8189C746A46F1BE2186CC125BFCABFB63989DB4E1C319247",
	"darwin/arm64":  "4D1BAEAA33A40E7D8189C746A46F1BE2186CC125BFCABFB63989DB4E1C319247",
}

// ReadBinary returns the raw bytes for the given os/arch key.
func ReadBinary(osArch string) ([]byte, error) {
	name, ok := BinaryNames[osArch]
	if !ok {
		return nil, nil
	}
	return binaries.ReadFile(name)
}

// ReadAndroidLibCxx returns the raw bytes of the bundled libc++_shared.so.
// The Android 7zz binary (a modern bionic build of 7-Zip) is dynamically
// linked against Termux's libc++. We extract this library next to the
// binary and point LD_LIBRARY_PATH at its directory so the binary resolves
// it on stock Android (which ships a differently-named platform libc++).
func ReadAndroidLibCxx() ([]byte, error) {
	return binaries.ReadFile("libc++_shared.so")
}
