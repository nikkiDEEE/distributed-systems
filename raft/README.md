# Project 4: Raft

## Introduction

This is an implementation of Raft, a replicated state machine protocol, which will be meant to use as a module. A replicated service achieves fault tolerance by storing complete copies of its state (i.e., data) on multiple replica servers. Replication allows the service to continue operating even if some of its servers experience failures (crashes or a broken or flaky network). The challenge is that failures may cause the replicas to hold differing copies of the data.

Raft organizes client requests into a sequence, called the log, and ensures that all the replica servers see the same log. Each replica executes client requests in log order, applying them to its local copy of the service's state. Since all the live replicas see the same log contents, they all execute the same requests in the same order, and thus continue to have identical service state. If a server fails but later recovers, Raft takes care of bringing its log up to date. Raft will continue to operate as long as at least a majority of the servers are alive and can talk to each other. If there is no such majority, Raft will make no progress, but will pick up where it left off as soon as a majority can communicate again.

This implementation of Raft is meant to be used as a module in a larger service. A set of Raft instances talk to each other with RPC to maintain replicated logs. This Raft interface will support an indefinite sequence of numbered commands, also called log entries. The entries are numbered with index numbers. The log entry with a given index will eventually be committed. At that point, Raft sends the log entry to the larger service for it to execute.

This design is based off [extended Raft paper](https://cs-people.bu.edu/liagos/651-2022/papers/raft-extended.pdf), with particular attention to Figure 2. This implementation includes everthing except cluster membership changes (Section 6) and log snapshotting.

## Run the code

### Part 4A: Leader Election

In this part the goal is for a single leader is elected, for the leader to remain the leader if there are no failures, and for a new leader to take over if the old leader fails or if packets to/from the old leader are lost. Run `go test -run 4A -race` to test the 4A code.

```
$ go test -run 4A -race
Test (4A): initial election ...
  ... Passed --   4.0  3   32    9170    0
Test (4A): election after network failure ...
  ... Passed --   6.1  3   70   13895    0
PASS
ok      raft    10.187s
```

### Part 4B: Log Replication and Persistence

If a Raft-based server reboots it should resume service where it left off. This requires that Raft keep persistent state that survives a reboot. The paper's Figure 2 mentions which state should be persistent. A real implementation would write Raft's persistent state to disk each time it changed, and would read the state from disk when restarting after a reboot. This implementation won't use the disk; instead, it will save and restore persistent state from a `Persister` object (see `persister.go`). You can check how much real time and CPU time this uses with the `time` command. Here's typical output:

```
$ time go test -run 4B
Test (4B): basic agreement ...
  ... Passed --   1.6  3   18    5158    3
Test (4B): RPC byte count ...
  ... Passed --   3.3  3   50  115122   11
Test (4B): agreement despite follower disconnection ...
  ... Passed --   6.3  3   64   17489    7
Test (4B): no agreement if too many followers disconnect ...
  ... Passed --   4.9  5  116   27838    3
Test (4B): concurrent Start()s ...
  ... Passed --   2.1  3   16    4648    6
Test (4B): rejoin of partitioned leader ...
  ... Passed --   8.1  3  111   26996    4
Test (4B): leader backs up quickly over incorrect follower logs ...
  ... Passed --  28.6  5 1342  953354  102
Test (4B): RPC counts aren't too high ...
... Passed --   3.4  3   30    9050   12
Test (4B): basic persistence ...
... Passed --   7.2  3  206   42208    6
Test (4B): more persistence ...
  ... Passed --  23.2  5 1194  198270   16
Test (4B): partitioned leader and one follower crash, leader restarts ...
  ... Passed --   3.2  3   46   10638    4
Test (4B): Figure 8 ...
  ... Passed --  35.1  5 9395 1939183   25
Test (4B): unreliable agreement ...
  ... Passed --   4.2  5  244   85259  246
Test (4B): Figure 8 (unreliable) ...
  ... Passed --  36.3  5 1948 4175577  216
Test (4B): churn ...
  ... Passed --  16.6  5 4402 2220926 1766
Test (4B): unreliable churn ...
  ... Passed --  16.5  5  781  539084  221
PASS
ok      cs350/raft      189.840s
go test -run 4B  10.32s user 5.81s system 8% cpu 3:11.40 total
```

The "ok raft 189.840s" means that Go measured the time taken for the 4B tests to be 189.840 seconds of real (wall-clock) time. The "10.32s user" means that the code consumed 10.32s seconds of CPU time, or time spent actually executing instructions (rather than waiting or sleeping). If it uses an unreasonable amount of time, look for time spent sleeping or waiting for RPC timeouts, loops that run without sleeping or waiting for conditions or channel messages, or large numbers of RPCs sent.
