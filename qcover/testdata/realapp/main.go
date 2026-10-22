// Command realapp is a binary for qcover's test of real-environment
// coverage: it runs retry or refund, as its argument says.
package main

import (
	"fmt"
	"os"
)

func retry() {
	fmt.Println("retrying")
}

func refund() {
	fmt.Println("refunding")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "retry" {
		retry()
		return
	}
	refund()
}
