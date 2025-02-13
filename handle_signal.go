package main

func handleSignal(sig SignalMsg, pc *webrtc.PeerConnection, conn *websocket.Conn) {
	log.Printf("Receive sig=%s\n", sig)
	switch sig.Type {
	case TypeOffer:
		log.Println("Получен offer")
		offer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  sig.SDP,
		}
		if err := pc.SetRemoteDescription(offer); err != nil {
			log.Println("Ошибка установки remote description:", err)
			return
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Println("Ошибка создания answer:", err)
			return
		}
		if err = pc.SetLocalDescription(answer); err != nil {
			log.Println("Ошибка установки локального описания:", err)
			return
		}
		<-webrtc.GatheringCompletePromise(pc)
		answerMsg := SignalMsg{
			Type: TypeAnswer,
			SDP:  pc.LocalDescription().SDP,
			To:   sig.From,
		}
		sendSignal(conn, answerMsg)
	case TypeAnswer:
		log.Println("Получен answer")
		answer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sig.SDP,
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			log.Println("Ошибка установки remote description:", err)
		}
	case TypeCandidate:
		// Для упрощения здесь кандидат не обрабатывается отдельно.
		log.Printf("Получен кандидат: %s", sig.Candidate)
	default:
		log.Printf("Неизвестный тип сигнала: %s", sig.Type)
	}
}
