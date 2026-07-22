//go:build !darwin

package main

import "fmt"

func main() {
	fmt.Println("menu bar app is only available on macOS")
}
