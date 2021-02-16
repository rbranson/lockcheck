package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

const PTRACE_O_EXITKILL = 1 << 20

const NICE_SYSCALL = 42069

const LINUX_X86_RAX = 10*8 // from arch/x86/entry/calling.h

func main() {
	// rumor has it you have to lock the OS thread if using ptrace calls
	runtime.LockOSThread()

	flag.Parse()

	args := flag.Args()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}

	err := cmd.Start()
	if err != nil {
		log.Fatal(err)
	}
	err = cmd.Wait()

	wpid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: add PTRACE_O_TRACEFORK PTRACE_O_TRACEVFORK
	err = syscall.PtraceSetOptions(wpid, PTRACE_O_EXITKILL|syscall.PTRACE_O_TRACECLONE|syscall.PTRACE_O_TRACESYSGOOD)
	if err != nil {
		log.Fatal(err)
	}

	killTs := time.Now().Add(5 * time.Second)

	// trap the next syscall operation
	err = syscall.PtraceSyscall(wpid, 0)
	if err != nil {
		log.Fatal(err)
	}

	for {
		var ws syscall.WaitStatus
		wpid, err = syscall.Wait4(-1*pgid, &ws, syscall.WALL, nil)
		if err != nil {
			log.Fatal(err)
		}

		if wpid == cmd.Process.Pid && ws.Exited() {
			log.Print("child exited\n")
			break
		}

		if ws.Exited() {
			log.Print("other exit\n")
			break
		}

		log.Printf("[%v] waitpid: trapcause=%v\n", wpid, ws.TrapCause())

		var regs syscall.PtraceRegs
		err = syscall.PtraceGetRegs(wpid, &regs)
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("[%v] syscall: %v(%v, %v, %v, %d, %v, %v)",
			wpid,
			regs.Orig_rax,
			regs.Rdi,
			regs.Rsi,
			regs.Rdx,
			regs.R10,
			regs.R8,
			regs.R9)

		if time.Now().After(killTs) {
			fmt.Printf("timeout\n")
			err = cmd.Process.Kill()
			if err != nil {
				log.Fatal(err)
			}
			break
		}

		if regs.Orig_rax == NICE_SYSCALL {
			killTs = time.Unix(int64(regs.Rdi), 0)
			log.Printf("killTs updated to %v\n", killTs)
		}

		// now run the syscall
		err = syscall.PtraceSyscall(wpid, 0)
		if err != nil {
			log.Fatal(err)
		}

		/*if regs.Orig_rax == NICE_SYSCALL {
			// this modifies the return register to intercept and replace the "not implemented" error
			_, _, err = syscall.Syscall(syscall.PTRACE_POKEUSR, uintptr(wpid), LINUX_X86_RAX*8, 0)
			if err != nil {
				log.Fatal(err)
			}
		}*/

		/*	if time.Now().After(killTs) {
				fmt.Print("timeout\n")
				err = cmd.Process.Kill()
				if err != nil {
					log.Fatal(err)
				}
				break
			}

			if regs.Orig_rax == NICE_SYSCALL {
				killTs = time.Unix(int64(regs.Rdi), 0)
				log.Printf("killTs updated to %v\n", killTs)
			}

			log.Printf("ptrace(PTRACE_SYSCALL) [2]\n")

			// now run the syscall
			err = syscall.PtraceSyscall(wpid, 0)
			if err != nil {
				log.Fatal(err)
			}

			log.Printf("wait4(%v) [2]\n", wpid)

			wpid, err = syscall.Wait4(wpid, nil, 0, nil)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("wait4 ret\n")

			/*if regs.Orig_rax == NICE_SYSCALL {
				// this modifies the return register to intercept and replace the "not implemented" error
				_, _, err = syscall.Syscall(syscall.PTRACE_POKEUSR, uintptr(wpid), LINUX_X86_RAX*8, 0)
				if err != nil {
					log.Fatal(err)
				}
			}*/
	}
}


