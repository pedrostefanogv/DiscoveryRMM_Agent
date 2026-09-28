//go:build !windows

package processutil

import "os"

// JobObject é um no-op fora do Windows (o agente desktop só existe no Windows).
type JobObject struct{}

func NewJobObject() (*JobObject, error) { return &JobObject{}, nil }

func (j *JobObject) Assign(*os.Process) error { return nil }

func (j *JobObject) Terminate() error { return nil }

func (j *JobObject) Close() {}
