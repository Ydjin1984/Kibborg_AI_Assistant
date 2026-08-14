//go:build !windows

package main

func pidListeningOnPort(port int) int { return 0 }

func spreadPIDAcrossCPUGroups(pid int) int { return 0 }

func setProcessAllCpuSets(pid int) bool { return false }

func spreadPIDTree(pid int) int { return 0 }
