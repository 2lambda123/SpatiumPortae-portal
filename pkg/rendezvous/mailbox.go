package rendezvous

import (
	"fmt"
	"net"
	"sync"

	"github.com/gorilla/websocket"
	"www.github.com/ZinoKader/portal/models"
	"www.github.com/ZinoKader/portal/models/protocol"
)

type Mailbox struct {
	Sender               *protocol.RendezvousSender
	Receiver             *protocol.RendezvousReceiver
	File                 models.File
	CommunicationChannel chan bool
}

type Mailboxes struct{ *sync.Map }

func (mailboxes *Mailboxes) StoreMailbox(p models.Password, m *Mailbox) {
	mailboxes.Store(p, m)
}

func (mailboxes *Mailboxes) GetMailbox(p models.Password) (*Mailbox, error) {
	mailbox, ok := mailboxes.Load(p)
	if !ok {
		return nil, fmt.Errorf("no mailbox with password '%s'", p)
	}
	return mailbox.(*Mailbox), nil
}

func (mailboxes *Mailboxes) DeleteMailbox(p models.Password) {
	mailboxes.Delete(p)
}

func NewClient(wsConn *websocket.Conn) *protocol.RendezvousClient {
	return &protocol.RendezvousClient{
		Conn: wsConn,
		IP:   wsConn.RemoteAddr().(*net.TCPAddr).IP,
	}
}