package pbservice

import "net"
import "fmt"
import "net/rpc"
import "log"
import "time"
import "viewservice"
import "sync"
import "sync/atomic"
import "os"
import "syscall"
import "math/rand"
import "errors"

type PBServer struct {
	mu         sync.Mutex
	l          net.Listener
	dead       int32 // for testing
	unreliable int32 // for testing
	me         string
	vs         *viewservice.Clerk
	// Your declarations here.
	view		 		viewservice.View
	storage			map[string] string
	record			map[int64] bool
	// LB					int32
}


func (pb *PBServer) Get(args *GetArgs, reply *GetReply) error {

	// Your code here.
	//if I'm the primary

	if pb.me == pb.view.Primary {
		v, ok := pb.storage[args.Key]
		if ok {
			reply.Value = v
			reply.Err = OK
	//		fmt.Println(v)
		}else{
			reply.Err = ErrNoKey
		}
	}else {
		reply.Err = ErrWrongServer
	}
	return nil
}

//Forward key value from primary to backup
func (pb *PBServer)	Forward(args *ForwardArgs, reply *ForwardReply) error {
	// fmt.Println("Forward",pb.view.Backup, pb.me)
	if pb.me == pb.view.Backup {
		pb.mu.Lock()
		if pb.record[args.Id] == true && args.Op == Append{
			reply.Err = OK
			pb.mu.Unlock()
			return nil
		}

		switch args.Op {
		case Put:
		// fmt.Println("Forward",args.Key, "youren ",args.Value, "youren ", args.Id, pb.me)
			pb.storage[args.Key] = args.Value
			reply.Err = OK
		case Append:
			// fmt.Println("Forward",args.Key, "youren ",args.Value, "youren ", args.Id, pb.me)
			pb.storage[args.Key] += args.Value
			reply.Err = OK
		}
		pb.record[args.Id] = true
		// fmt.Println("forward-backup",pb.storage)
		pb.mu.Unlock()
	}
	return nil
}
//handle of forward
func (pb *PBServer) HandleForward(key string, value string, op string, id int64)error {
	args := &ForwardArgs{}
	args.Op = op
	args.Key = key
	args.Value = value
	args.Id = id
	var reply PutAppendReply

	// fmt.Println("forward-primary",pb.storage)
	ok := call(pb.view.Backup, "PBServer.Forward", args, &reply)
	if ok == true && reply.Err == OK{
		return nil
	}else {
		// fmt.Println(args.Key)
		return errors.New("Forward error")
	}
}

func (pb *PBServer) Dup(args *DupArgs, reply *DupReply) error {
	if pb.me == pb.view.Backup {
		pb.mu.Lock()
		for key, value := range args.Storage{
			pb.storage[key] = value
		}
		for key, value := range args.Record{
			pb.record[key] = value
		}
		//  fmt.Println("dup",pb.storage,pb.me)
		pb.mu.Unlock()
		reply.Err = OK
	}
	return nil
}
func (pb *PBServer) HandleDup(des string) error{
	args := &DupArgs{}
	args.Storage = pb.storage
	args.Record = pb.record
	var reply DupReply
	// pb.mu.Lock()
	// defer pb.mu.Unlock()
	//	fmt.Println("dup-first",pb.storage)
	//Sleep for a while or else will failed
	time.Sleep(viewservice.PingInterval)

	ok := call(des, "PBServer.Dup", args, &reply)
	if ok == true && reply.Err == OK {
		return nil
	}
	return errors.New("Dup error")
}
func (pb *PBServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) error {

	// Your code here.

	//Put should be handle by primaryk
	if pb.me != pb.view.Primary {
		reply.Err = ErrWrongServer
		return nil
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	//  fmt.Println("PutAppend",args.Key, args.Value)
	id := args.Id
	if pb.record[id] == true {
		reply.Err = OK
		return nil
	}

	if pb.view.Backup != "" {
		err := pb.HandleForward(args.Key, args.Value, args.Op, args.Id);
		if err != nil{
			reply.Err = ErrWrongServer;
			return errors.New("forward error")
		}
	}

	switch args.Op {
	case Put:
		// fmt.Println("Put",args.Key,args.Value,pb.view.Backup)
		pb.storage[args.Key] = args.Value
		reply.Err = OK
	case Append:
		// fmt.Println(args.Key, "youren ",args.Value, "youren ", args.Id, pb.me)
		pb.storage[args.Key] += args.Value
		reply.Err = OK
	}
	pb.record[id] = true
	// fmt.Println(args.id,id)
	return nil
}


//
// ping the viewserver periodically.
// if view changed:
//   transition to new view.
//   manage transfer of state from primary to new backup.
//
func (pb *PBServer) tick() {

	// Your code here.
	vw,_ := pb.vs.Ping(pb.view.Viewnum)
pb.mu.Lock()
defer pb.mu.Unlock()
	//View changed
	if pb.view.Viewnum != vw.Viewnum {
		//I'm the new Backup now
		// fmt.Println("view changed", vw, pb.view, pb.me)
	 	if pb.me == vw.Primary {
			if vw.Backup != pb.view.Backup && vw.Backup != "" {
				pb.HandleDup(vw.Backup)
			}
		}
		//when The backup or primary is ready, update view and receive request
		pb.view = vw
	}
}

// tell the server to shut itself down.
// please do not change these two functions.
func (pb *PBServer) kill() {
	atomic.StoreInt32(&pb.dead, 1)
	pb.l.Close()
}

// call this to find out if the server is dead.
func (pb *PBServer) isdead() bool {
	return atomic.LoadInt32(&pb.dead) != 0
}

// please do not change these two functions.
func (pb *PBServer) setunreliable(what bool) {
	if what {
		atomic.StoreInt32(&pb.unreliable, 1)
	} else {
		atomic.StoreInt32(&pb.unreliable, 0)
	}
}

func (pb *PBServer) isunreliable() bool {
	return atomic.LoadInt32(&pb.unreliable) != 0
}


func StartServer(vshost string, me string) *PBServer {
	pb := new(PBServer)
	pb.me = me
	pb.vs = viewservice.MakeClerk(me, vshost)
	// Your pb.* initializations here.
	pb.storage = make(map[string] string)
	pb.record  = make(map[int64] bool)
	pb.view.Viewnum = 0
	pb.view.Primary = ""
	pb.view.Backup = ""

	rpcs := rpc.NewServer()
	rpcs.Register(pb)

	os.Remove(pb.me)
	l, e := net.Listen("unix", pb.me)
	if e != nil {
		log.Fatal("listen error: ", e)
	}
	pb.l = l

	// please do not change any of the following code,
	// or do anything to subvert it.

	go func() {
		for pb.isdead() == false {
			conn, err := pb.l.Accept()
			if err == nil && pb.isdead() == false {
				if pb.isunreliable() && (rand.Int63()%1000) < 100 {
					// discard the request.
					conn.Close()
				} else if pb.isunreliable() && (rand.Int63()%1000) < 200 {
					// process the request but force discard of reply.
					c1 := conn.(*net.UnixConn)
					f, _ := c1.File()
					err := syscall.Shutdown(int(f.Fd()), syscall.SHUT_WR)
					if err != nil {
						fmt.Printf("shutdown: %v\n", err)
					}
					go rpcs.ServeConn(conn)
				} else {
					go rpcs.ServeConn(conn)
				}
			} else if err == nil {
				conn.Close()
			}
			if err != nil && pb.isdead() == false {
				fmt.Printf("PBServer(%v) accept: %v\n", me, err.Error())
				pb.kill()
			}
		}
	}()

	go func() {
		for pb.isdead() == false {
			pb.tick()
			time.Sleep(viewservice.PingInterval)
		}
	}()

	return pb
}
