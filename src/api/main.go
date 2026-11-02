package main

import (
	"github.com/llllogan/lab-controller/internal/controller"
	"log"
)

func main() {
	if err := controller.Run(); err != nil {
		log.Fatal(err)
	}
}
