// handlers.go sepcifies the websocket handlers that rendezvous server uses to facilitate communcation between sender and receiver.
package rendezvous

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SpatiumPortae/portal/internal/conn"
	"github.com/SpatiumPortae/portal/protocol/rendezvous"
	"github.com/SpatiumPortae/portal/tools"
)

// handleEstablishSender returns a websocket handler that communicates with the sender.
func (s *Server) handleEstablishSender() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		ctx := r.Context()
		c, err := tools.FromContext(ctx)

		if err != nil {
			// TODO: do ask me
			http.Error(w, "don't ask me", http.StatusInternalServerError)
			return
		}
		rc := conn.Rendezvous{Conn: c}

		// Bind an ID to this communication and send ot to the sender
		id := s.ids.Bind()
		defer func() { s.ids.Delete(id) }()
		rc.WriteMsg(rendezvous.Msg{
			Type: rendezvous.RendezvousToSenderBind,
			Payload: rendezvous.Payload{
				ID: id,
			},
		})

		msg, err := rc.ReadMsg(rendezvous.SenderToRendezvousEstablish)
		if err != nil {
			log.Println("message did not follow protocol:", err)
			return
		}

		// Allocate a mailbox for this communication.
		mailbox := &Mailbox{
			CommunicationChannel: make(chan []byte),
			Quit:                 make(chan bool),
		}
		s.mailboxes.StoreMailbox(msg.Payload.Password, mailbox)
		password := msg.Payload.Password

		// wait for receiver to connect
		timeout := time.NewTimer(RECEIVER_CONNECT_TIMEOUT)
		select {
		case <-timeout.C:
			return
		case <-mailbox.CommunicationChannel:
			// receiver connected
			break
		}

		rc.WriteMsg(rendezvous.Msg{
			Type: rendezvous.RendezvousToSenderReady,
		})

		msg, err = rc.ReadMsg(rendezvous.SenderToRendezvousPAKE)
		if err != nil {
			log.Println("message did not follow protocol:", err)
			return
		}
		// send PAKE bytes to receiver
		mailbox.CommunicationChannel <- msg.Payload.Bytes
		// respond with receiver PAKE bytes
		rc.WriteMsg(rendezvous.Msg{
			Type: rendezvous.RendezvousToSenderPAKE,
			Payload: rendezvous.Payload{
				Bytes: <-mailbox.CommunicationChannel,
			},
		})

		msg, err = rc.ReadMsg(rendezvous.SenderToRendezvousSalt)
		if err != nil {
			log.Println("message did not follow protocol:", err)
			return
		}

		// Send the salt to the receiver.
		mailbox.CommunicationChannel <- msg.Payload.Salt
		// Start the relay of messgaes between the sender and receiver handlers.
		startRelay(s, rc, mailbox, password)
	}
}

// handleEstablishReceiver returns a websocket handler that that communicates with the sender.
func (s *Server) handleEstablishReceiver() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := tools.FromContext(r.Context())
		if err != nil {
			// TODO: do ask me
			http.Error(w, "don't ask me", http.StatusInternalServerError)
			return
		}
		rc := conn.Rendezvous{Conn: c}

		// Establish receiver.
		msg, err := rc.ReadMsg(rendezvous.ReceiverToRendezvousEstablish)
		if err != nil {
			log.Println("message did not follow protocol:", err)
			return
		}

		mailbox, err := s.mailboxes.GetMailbox(msg.Payload.Password)
		if err != nil {
			log.Println("failed to get mailbox:", err)
			return
		}
		if mailbox.hasReceiver {
			log.Println("mailbox already has a receiver:", err)
			return
		}
		// this receiver was first, reserve this mailbox for it to receive
		mailbox.hasReceiver = true
		s.mailboxes.StoreMailbox(msg.Payload.Password, mailbox)
		password := msg.Payload.Password

		// notify sender we are connected
		mailbox.CommunicationChannel <- nil
		// send back received sender PAKE bytes
		rc.WriteMsg(rendezvous.Msg{
			Type: rendezvous.RendezvousToReceiverPAKE,
			Payload: rendezvous.Payload{
				Bytes: <-mailbox.CommunicationChannel,
			},
		})

		msg = rendezvous.Msg{}
		msg, err = rc.ReadMsg(rendezvous.ReceiverToRendezvousPAKE)
		if err != nil {
			log.Println("message did not follow protocol:", err)
			return
		}

		mailbox.CommunicationChannel <- msg.Payload.Bytes
		rc.WriteMsg(rendezvous.Msg{
			Type: rendezvous.RendezvousToReceiverSalt,
			Payload: rendezvous.Payload{
				Salt: <-mailbox.CommunicationChannel,
			},
		})
		startRelay(s, rc, mailbox, password)
	}
}

// starts the relay service, closing it on request (if i.e. clients can communicate directly)
func startRelay(s *Server, wsConn *websocket.Conn, mailbox *Mailbox, mailboxPassword string) {
	relayForwardCh := make(chan []byte)
	// listen for incoming websocket messages from currently handled client
	go func() {
		for {
			_, p, err := wsConn.ReadMessage()
			if err != nil {
				log.Println("error when listening to incoming client messages:", err)
				fmt.Printf("closed by: %s\n", wsConn.RemoteAddr())
				mailbox.Quit <- true
				return
			}
			relayForwardCh <- p
		}
	}()

	for {
		select {
		// received payload from __other client__, relay it to our currently handled client
		case relayReceivePayload := <-mailbox.CommunicationChannel:
			wsConn.WriteMessage(websocket.BinaryMessage, relayReceivePayload)

		// received payload from __currently handled__ client, relay it to other client
		case relayForwardPayload := <-relayForwardCh:
			msg := rendezvous.Msg{}
			err := json.Unmarshal(relayForwardPayload, &msg)
			// failed to unmarshal, we are in (encrypted) relay-mode, forward message directly to client
			if err != nil {
				mailbox.CommunicationChannel <- relayForwardPayload
			} else {
				// close the relay service if sender requested it
				if isExpected(msg.Type, rendezvous.ReceiverToRendezvousClose) {
					mailbox.Quit <- true
					return
				}
			}

		// deallocate mailbox and quit
		case <-mailbox.Quit:
			s.mailboxes.Delete(mailboxPassword)
			return
		}
	}
}

// isExpected is a convenience helper function that checks message types and logs errors.
func isExpected(actual rendezvous.MsgType, expected rendezvous.MsgType) bool {
	wasExpected := actual == expected
	if !wasExpected {
		log.Printf("Expected message of type: %d. Got type %d\n", expected, actual)
	}
	return wasExpected
}
