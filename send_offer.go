package main

func SendOffer() {
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		log.Fatal(err)
	}
	dataChannel = dc
	dc.OnOpen(func() {
		log.Println("DataChannel открыт!")
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		log.Printf("Прямое сообщение получено: %s", string(msg.Data))
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}
	if err = pc.SetLocalDescription(offer); err != nil {
		log.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(pc)
	offerMsg := SignalMsg{
		Type: TypeOffer,
		SDP:  pc.LocalDescription().SDP,
	}
	log.Printf("Going to send sig=%s\n", sig)
	data, err := json.Marshal(sig)
	if err != nil {
		log.Println("Ошибка маршалинга сигнала:", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Println("Ошибка отправки сигнала:", err)
	}

}
