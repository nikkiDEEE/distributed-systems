package raft

//
// This is an outline of the API that raft must expose to
// the service (or tester). See comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   Create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   Start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   Each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester) in the same server.
//

import (
	"bytes"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"cs350/labgob"
	"cs350/labrpc"
)

// As each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). Set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int
}

type LogEntry struct {
	Term    int
	Index   int
	Command interface{}
}

type State int

const (
	Follower State = iota
	Candidate
	Leader
)

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // This peer's index into peers[]
	dead      int32               // Set by Kill()

	// Your data here (4A, 4B).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	applyCh chan ApplyMsg

	// Persistent state on all servers
	currentTerm int
	votedFor    int
	log         []LogEntry

	// Volatile state on all servers
	commitIndex     int
	lastApplied     int
	state           State
	electionTimeout time.Time
	voteCount       int

	// Volatile state on leaders
	nextIndex  []int
	matchIndex []int
}

// Return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	// Your code here (4A).
	rf.mu.Lock()
	defer rf.mu.Unlock()

	term := rf.currentTerm
	isLeader := (rf.state == Leader)

	return term, isLeader
}

// Save Raft's persistent state to stable storage, where it
// can later be retrieved after a crash and restart. See paper's
// Figure 2 for a description of what should be persistent.
func (rf *Raft) persist() {
	// Your code here (4B).
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)

	// Encode persistent state on all servers
	if (e.Encode(rf.currentTerm) != nil) || (e.Encode(rf.votedFor) != nil) || (e.Encode(rf.log) != nil) {
		panic("Failed to write persisted state")
	}

	data := w.Bytes()
	rf.persister.SaveRaftState(data)
}

// Restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)

	var currentTerm, votedFor int
	var logs []LogEntry

	if (d.Decode(&currentTerm) != nil) || (d.Decode(&votedFor) != nil) || (d.Decode(&logs) != nil) {
		panic("Failed to read persisted state")
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = logs
	}
}

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term          int
	Success       bool
	ConflictIndex int
	ConflictTerm  int
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	rf.resetElectionTimeout()
	reply.Term = rf.currentTerm
	reply.Success = false

	if args.LeaderId == rf.me || args.Term < rf.currentTerm {
		return // reject if rpc call to leader/stale term
	}

	if rf.getLastLogIndex() < args.PrevLogIndex {
		reply.ConflictIndex = rf.getLastLogIndex() + 1
		return
	}

	if args.Term > rf.currentTerm || (rf.state == Candidate && args.Term >= rf.currentTerm) {
		rf.state = Follower // revert to raft state
		rf.persist()
		rf.currentTerm = args.Term // update term
		rf.votedFor = -1           // reset vote
	}

	var prevLogTerm int
	if args.PrevLogIndex > 0 {
		prevLogTerm = rf.log[args.PrevLogIndex-1].Term
	}

	if prevLogTerm != args.PrevLogTerm {
		reply.ConflictTerm = prevLogTerm
		for i := args.PrevLogIndex - 1; i >= 0; i-- {
			reply.ConflictIndex = rf.log[i].Index
			if rf.log[i].Term != prevLogTerm {
				break // find conflict for all indexes within leader's term
			}
		}
		return
	}

	reply.Success = true
	if len(args.Entries) != 0 {
		currLog := rf.log[args.PrevLogIndex:]
		i := 0
		for i < min(len(currLog), len(args.Entries)) {
			if currLog[i].Term != args.Entries[i].Term {
				rf.log = rf.log[:args.PrevLogIndex+i] // update logs to match leader
				break
			}
			i++
		}
		rf.log = append(rf.log, args.Entries[i:]...)
		rf.persist()
	}

	if args.LeaderCommit > rf.commitIndex {
		rf.commitIndex = min(args.LeaderCommit, rf.getLastLogIndex())
		select {
		case rf.applyCh <- ApplyMsg{true, args.Entries, rf.commitIndex}:
		default:
		}
	}
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

func (rf *Raft) handleAppendEntriesReply(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if reply.Term > rf.currentTerm { // for leader to update itself
		rf.state = Follower
		rf.persist()
		rf.currentTerm = reply.Term
		rf.votedFor = -1
		return
	}

	if !reply.Success {
		if reply.ConflictIndex != 0 && reply.ConflictTerm == 0 {
			rf.nextIndex[server] = reply.ConflictIndex
		} else {
			for lastIndex := len(rf.log) - 1; lastIndex >= 0; lastIndex-- {
				var index, term int
				switch {
				case lastIndex >= 0:
					index = rf.log[lastIndex].Index
					term = rf.log[lastIndex].Term
				case len(rf.log) > 0:
					index = rf.getLastLogIndex()
					term = rf.getLastLogTerm()
				}

				switch {
				case term == reply.ConflictTerm:
					rf.nextIndex[server] = index + 1
				case term < reply.ConflictTerm:
					rf.nextIndex[server] = reply.ConflictIndex
				}
			}
		}
		return
	}

	newLastIndex := args.PrevLogIndex + len(args.Entries)
	if newLastIndex+1 > rf.nextIndex[server] {
		rf.nextIndex[server] = newLastIndex + 1
	}
	if newLastIndex > rf.matchIndex[server] {
		rf.matchIndex[server] = newLastIndex
	}

	if rf.getLastLogIndex() > rf.commitIndex {
		for j := rf.commitIndex; j < len(rf.log); j++ {
			if rf.log[j].Term == rf.currentTerm {
				var replicas int
				for peer := range rf.peers {
					if rf.matchIndex[peer] > j {
						replicas++
					}
				}

				if replicas > len(rf.peers)/2 { // commit if majority of servers have replicated
					rf.commitIndex = j + 1
					select { // dont block
					case rf.applyCh <- ApplyMsg{true, 0, 0}:
					default:
					}
				}
			}
		}
	}
}

// Example RequestVote RPC arguments structure.
// Field names must start with capital letters!
type RequestVoteArgs struct {
	// Your data here (4A, 4B).
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

// Example RequestVote RPC reply structure.
// Field names must start with capital letters!
type RequestVoteReply struct {
	// Your data here (4A).
	Term        int
	VoteGranted bool
}

// Example RequestVote RPC handler.
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (4A, 4B).
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.VoteGranted = false

	if args.Term < rf.currentTerm || args.CandidateId == rf.me {
		return
	}

	if args.Term > rf.currentTerm { // acknowledge that a server is trying to become the server
		rf.state = Follower // revert raft state
		rf.persist()
		rf.currentTerm = args.Term // update term
		rf.votedFor = -1           // reset vote
	}

	if rf.isUpToDate(args.LastLogTerm, args.LastLogIndex) && (rf.votedFor == -1 || rf.votedFor == args.CandidateId) {
		rf.votedFor = args.CandidateId
		reply.VoteGranted = true
		time.Sleep(300 * time.Millisecond)
	}
	rf.resetElectionTimeout()
}

// Checks if candidate’s log is at least as up-to-date as receiver’s log
func (rf *Raft) isUpToDate(candidateTerm int, candidateIndex int) bool {
	voterTerm, voterIndex := rf.getLastLogTerm(), rf.getLastLogIndex()
	return candidateTerm > voterTerm || (candidateTerm == voterTerm && candidateIndex >= voterIndex)
}

func (rf *Raft) handleVoteReply(reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if reply.VoteGranted {
		rf.voteCount += 1
		if rf.voteCount > len(rf.peers)/2 {
			rf.state = Leader
			// fmt.Printf("Server %v is the leader now, with entries %v\n", rf.me, rf.log)
			rf.persist()
			rf.nextIndex = make([]int, len(rf.peers))
			rf.matchIndex = make([]int, len(rf.peers)) // initializes all to 0
			rf.matchIndex[rf.me] = rf.getLastLogIndex()
			nextIndex := rf.getLastLogIndex() + 1 // leader last log index + 1
			for i := range rf.nextIndex {
				rf.nextIndex[i] = nextIndex
			}
			go rf.sendHeartbeats()
		}
	}
}

// Example code to send a RequestVote RPC to a server.
// Server is the index of the target server in rf.peers[].
// Expects RPC arguments in args. Fills in *reply with RPC reply,
// so caller should pass &reply.
//
// The types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// Look at the comments in ../labrpc/labrpc.go for more details.
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

// The service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. If this
// server isn't the leader, returns false. Otherwise start the
// agreement and return immediately. There is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. Even if the Raft instance has been killed,
// this function should return gracefully.
//
// The first return value is the index that the command will appear at if it's ever committed. (curr server next index)
// The second return value is the current term.
// The third return value is true if this server believes it is the leader.
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := true

	// Your code here (4B).
	rf.mu.Lock()
	term = rf.currentTerm

	if rf.state != Leader {
		rf.mu.Unlock()
		isLeader = false
		return index, term, isLeader
	}

	index = rf.nextIndex[rf.me]
	entry := LogEntry{
		Term:    term,
		Index:   index,
		Command: command,
	}
	rf.log = append(rf.log, entry) // leader appends command as new log entry
	rf.nextIndex[rf.me] += 1
	rf.matchIndex[rf.me] = index
	rf.persist()
	rf.mu.Unlock()

	go rf.sendHeartbeats() // then issues AppendEntries RPC

	return index, term, true
}

// The tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. Your code can use killed() to
// check whether Kill() has been called. The use of atomic avoids the
// need for a lock.
//
// The issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. Any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

func (rf *Raft) sendHeartbeats() {
	for !rf.killed() {
		rf.mu.Lock()

		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}

		for i := range rf.peers {
			if i != rf.me {
				go func(server int) {
					rf.heartbeat(server) // dispatch hb to all servers
				}(i)
			}
		}

		rf.mu.Unlock()
		time.Sleep(550 * time.Millisecond)
	}
}

func (rf *Raft) heartbeat(server int) {
	if !rf.killed() {
		rf.mu.Lock()

		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}

		args := &AppendEntriesArgs{
			Term:         rf.currentTerm,
			LeaderId:     rf.me,
			PrevLogIndex: rf.nextIndex[server] - 1,
			PrevLogTerm:  0,
			Entries:      nil,
			LeaderCommit: rf.commitIndex,
		}

		if args.PrevLogIndex > 0 {
			args.PrevLogTerm = rf.log[args.PrevLogIndex-1].Term
		}

		if rf.getLastLogIndex() >= rf.nextIndex[server] {
			args.Entries = rf.log[rf.nextIndex[server]-1:]
		}

		rf.mu.Unlock()

		reply := &AppendEntriesReply{}
		if ok := rf.sendAppendEntries(server, args, reply); ok {
			rf.handleAppendEntriesReply(server, args, reply)
		}
	}
}

func (rf *Raft) startElection() {
	rf.mu.Lock()
	args := &RequestVoteArgs{
		Term:        rf.currentTerm,
		CandidateId: rf.me,
	}
	if len(rf.log) > 0 {
		args.LastLogTerm = rf.getLastLogTerm()
		args.LastLogIndex = rf.getLastLogIndex()
	}
	rf.mu.Unlock()

	for i := range rf.peers {
		if i != rf.me && !rf.killed() {
			go func(server int) {
				reply := &RequestVoteReply{}
				if ok := rf.sendRequestVote(server, args, reply); ok {
					rf.handleVoteReply(reply)
				}
			}(i)
		}
	}
}

// The ticker go routine starts a new election if this peer hasn't received
// heartsbeats recently.
func (rf *Raft) ticker() {
	for !rf.killed() {
		time.Sleep(time.Duration(rand.Intn(350)+350) * time.Millisecond) // ticker loop within LL of electionTimeout

		rf.mu.Lock()
		if time.Now().Before(rf.electionTimeout) {
			rf.mu.Unlock()
			continue
		}

		// Election timeout expired
		rf.state = Candidate // convert to candidate
		rf.persist()
		rf.currentTerm += 1       // increment currentTerm
		rf.votedFor = rf.me       // vote for self
		rf.voteCount = 1          // increment vote count
		rf.resetElectionTimeout() // reset election timer
		rf.mu.Unlock()

		// fmt.Printf("Server %v starting re-election on term %v\n", rf.me, rf.currentTerm)
		rf.startElection()
	}
}

func (rf *Raft) getLastLogTerm() int {
	if len(rf.log) == 0 {
		return 0
	}
	return rf.log[len(rf.log)-1].Term
}

func (rf *Raft) getLastLogIndex() int {
	if len(rf.log) == 0 {
		return 0
	}
	return rf.log[len(rf.log)-1].Index
}

// commits entries upon receiving applyCh, blocks otherwise
func (rf *Raft) commitEntries(applyCh chan<- ApplyMsg) {
	for !rf.killed() {
		applyFlag := <-rf.applyCh
		if applyFlag.CommandValid {
			rf.mu.Lock()
			for rf.commitIndex > rf.lastApplied {
				i := rf.lastApplied + 1
				commitLog := rf.log[rf.lastApplied].Command
				rf.lastApplied += 1
				rf.mu.Unlock()
				applyCh <- ApplyMsg{true, commitLog, i}
				rf.mu.Lock()
			}
			rf.mu.Unlock()
		} else {
			rf.mu.Lock()
			applyCh <- ApplyMsg{false, 0, 0}
			rf.mu.Unlock()
		}
	}
}

func (rf *Raft) resetElectionTimeout() {
	rf.electionTimeout = time.Now().Add(time.Duration(650+rand.Intn(350)) * time.Millisecond)
}

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// The service or tester wants to create a Raft server. The ports
// of all the Raft servers (including this one) are in peers[]. This
// server's port is peers[me]. All the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (4A, 4B).
	rf.applyCh = make(chan ApplyMsg)

	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = []LogEntry{}

	rf.commitIndex = 0
	rf.lastApplied = 0
	rf.state = Follower
	rf.voteCount = 0
	rf.resetElectionTimeout()

	rf.nextIndex = nil
	rf.matchIndex = nil

	// initialize from state persisted before a crash.
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections.
	go rf.ticker()

	go rf.commitEntries(applyCh)

	applyCh <- ApplyMsg{true, 0, 0}

	return rf
}
