package netstatecl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

type operation int

const (
	OpConsumerState = iota // Args: [[IP 1, tag 1, workerIdx 1], [IP 2, tag 2, workerIdx 2]..[IP n, tag n, workerIdx n]]
)

// TODO: add a Lamport timestamp?
type Instruction struct {
	Op   operation
	Args []string
	Tag  string
}

func EncodeArgsOpConsumerState(args [][]string) []string {
	var encoded []string

	for _, arg := range args {
		encoded = append(encoded, strings.Join(arg, ","))
	}

	return encoded
}

func DecodeArgsOpConsumerState(args []string) [][]string {
	var decoded [][]string

	for _, arg := range args {
		decoded = append(decoded, strings.Split(arg, ","))
	}

	return decoded
}

func Exec(httpClient *http.Client, netstated string, inst *Instruction) error {
	serialized, err := json.Marshal(inst)
	if err != nil {
		return err
	}

	if httpClient == nil {
		httpClient = &http.Client{}
	}
	res, err := httpClient.Post(netstated, "application/json; charset=UTF-8", bytes.NewBuffer(serialized))
	if err != nil {
		return err
	}

	defer res.Body.Close()
	return nil
}
