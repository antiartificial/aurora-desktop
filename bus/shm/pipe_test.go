package shm

import "os"

func osPipe() (*os.File, *os.File, error) { return os.Pipe() }
