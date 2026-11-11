package main

import (
	"os"

	postgres "github.com/databasifylabs/engine-postgres/engine"
	sdk "github.com/databasifylabs/engine-sdk"
)

func main() {
	os.Exit(sdk.Run(postgres.New()))
}
