package main

// cmd /S strips the outer pair of quotes after /C. The inner pair keeps a
// batch file path with spaces or shell metacharacters as one executable path.
func batchHookCommandLine(path string) string {
	return `cmd.exe /D /S /C ""` + path + `""`
}
