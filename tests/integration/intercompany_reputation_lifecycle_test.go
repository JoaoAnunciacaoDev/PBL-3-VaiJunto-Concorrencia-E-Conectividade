package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
)

// registerAndLoginDriverViaTCP cria e autentica um motorista com veículo via TCP.
func registerAndLoginDriverViaTCP(t *testing.T, tcpAddr, name, email, password, plate string) (*TestTCPClient, protocol.UserResponse) {
	t.Helper()
	cli, err := ConnectTestTCPClient(tcpAddr)
	if err != nil {
		t.Fatalf("conectar motorista %s: %v", email, err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	regResp, err := cli.Send(protocol.ActionRegisterUser, protocol.CreateUserRequest{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     models.RoleDriver,
	})
	if err != nil || regResp.Success != "success" {
		t.Fatalf("registrar motorista %s: err=%v msg=%s", email, err, regResp.Message)
	}

	loginResp, err := cli.Send(protocol.ActionLogin, protocol.LoginRequest{
		Email:    email,
		Password: password,
	})
	if err != nil || loginResp.Success != "success" {
		t.Fatalf("logar motorista %s: err=%v msg=%s", email, err, loginResp.Message)
	}

	var driverUser protocol.UserResponse
	if err := json.Unmarshal(loginResp.Payload, &driverUser); err != nil {
		t.Fatalf("decodificar driverUser: %v", err)
	}

	vehResp, err := cli.Send(protocol.ActionRegisterVehicle, protocol.CreateVehicleRequest{
		Plate:        plate,
		Model:        "Sedan Consorcio",
		Color:        "Prata",
		SeatCapacity: 4,
	})
	if err != nil || vehResp.Success != "success" {
		t.Fatalf("registrar veículo: err=%v msg=%s", err, vehResp.Message)
	}

	return cli, driverUser
}

func TestIntercompanyPassengersRatingsComplaintsAndRideCancellation(t *testing.T) {
	cluster := NewTestCluster(t, 5*time.Second)
	now := time.Now().Add(24 * time.Hour).Truncate(time.Hour)

	// Motoristas em Empresa A e Empresa B via TCP
	drvAClient, drvA := registerAndLoginDriverViaTCP(t, cluster.Nodes["empresa-a"].Server.TCPAddr(),
		"Ana Motorista", "ana-inter@vaijunto.test", "Senha@123", "ANA-1A01")
	drvBClient, drvB := registerAndLoginDriverViaTCP(t, cluster.Nodes["empresa-b"].Server.TCPAddr(),
		"Bruno Motorista", "bruno-inter@vaijunto.test", "Senha@123", "BRU-2B02")

	// Carona A: Salvador -> Feira de Santana
	createAResp, err := drvAClient.Send(protocol.ActionCreateRide, protocol.CreateRideRequest{
		DepartureAt: now,
		Segments: []protocol.CreateStageRequest{
			{
				Origin: enum.Salvador, Destination: enum.FeiraDeSantana,
				DepartureAt: now, ArrivalAt: now.Add(90 * time.Minute),
				PriceCents: 3500, AvailableSeats: 3,
			},
		},
	})
	if err != nil || createAResp.Success != "success" {
		t.Fatalf("criar carona A: err=%v msg=%s", err, createAResp.Message)
	}
	var rideA models.Ride
	_ = json.Unmarshal(createAResp.Payload, &rideA)

	// Carona B: Feira de Santana -> Lençóis
	createBResp, err := drvBClient.Send(protocol.ActionCreateRide, protocol.CreateRideRequest{
		DepartureAt: now.Add(2 * time.Hour),
		Segments: []protocol.CreateStageRequest{
			{
				Origin: enum.FeiraDeSantana, Destination: enum.Lencois,
				DepartureAt: now.Add(2 * time.Hour), ArrivalAt: now.Add(5 * time.Hour),
				PriceCents: 6500, AvailableSeats: 3,
			},
		},
	})
	if err != nil || createBResp.Success != "success" {
		t.Fatalf("criar carona B: err=%v msg=%s", err, createBResp.Message)
	}
	var rideB models.Ride
	_ = json.Unmarshal(createBResp.Payload, &rideB)

	// Passageiro cadastrado exclusivamente na Empresa A
	paxClient, err := RegisterAndLoginPassenger(
		cluster.Nodes["empresa-a"].Server.TCPAddr(),
		"Alice Passageira",
		"alice-inter@vaijunto.test",
		"Senha@123",
	)
	if err != nil {
		t.Fatalf("registrar passageiro: %v", err)
	}
	defer paxClient.Close()

	// Obtém ID do passageiro via get_my_profile
	profResp, _ := paxClient.Send(protocol.ActionGetMyProfile, nil)
	var paxUser protocol.UserResponse
	_ = json.Unmarshal(profResp.Payload, &paxUser)

	// 1. Passageiro da Empresa A reserva itinerário multi-empresa (Trecho A + Trecho B)
	bookResp, err := paxClient.Send(protocol.ActionConfirmReservation, protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{RideID: rideA.ID, SegmentID: rideA.Segments[0].ID, CompanyID: "empresa-a"},
			{RideID: rideB.ID, SegmentID: rideB.Segments[0].ID, CompanyID: "empresa-b"},
		},
	})
	if err != nil || bookResp.Success != "success" {
		t.Fatalf("reserva multi-empresa falhou: err=%v msg=%s", err, bookResp.Message)
	}
	var res protocol.ReservationResponse
	_ = json.Unmarshal(bookResp.Payload, &res)

	// 2. Motorista B na Empresa B DEVE conseguir visualizar a passageira Alice da Empresa A!
	paxListResp, err := drvBClient.Send(protocol.ActionGetRidePassengers, protocol.GetRidePassengersRequest{
		RideID: rideB.ID,
	})
	if err != nil || paxListResp.Success != "success" {
		t.Fatalf("get_ride_passengers na empresa B falhou: err=%v msg=%s", err, paxListResp.Message)
	}
	var paxList protocol.RidePassengersResponse
	_ = json.Unmarshal(paxListResp.Payload, &paxList)
	if len(paxList.Segments) != 1 || len(paxList.Segments[0].Passengers) != 1 {
		t.Fatalf("esperado 1 passageiro remoto visível na empresa B, obtido: %+v", paxList)
	}
	if paxList.Segments[0].Passengers[0].ID != paxUser.ID || paxList.Segments[0].Passengers[0].CompanyID != "empresa-a" {
		t.Fatalf("dados do passageiro remoto incorretos: %+v", paxList.Segments[0].Passengers[0])
	}

	// 3. Sem driver_id/ride_id/segment_id explícitos, rate_driver em reserva com múltiplas caronas deve orientar seleção
	ambiguousRate, _ := paxClient.Send(protocol.ActionRateDriver, protocol.RateDriverRequest{
		ReservationID: res.ID,
		Score:         5,
		Comment:       "Qual motorista?",
	})
	if ambiguousRate.Success != "error" {
		t.Fatalf("esperava erro orientando escolha de trecho/motorista em reserva com múltiplas caronas")
	}

	// 4. Motorista B conclui seu trecho (finish_segment) na Empresa B
	finBResp, err := drvBClient.Send(protocol.ActionFinishSegment, protocol.FinishSegmentRequest{
		RideID:    rideB.ID,
		SegmentID: rideB.Segments[0].ID,
	})
	if err != nil || finBResp.Success != "success" {
		t.Fatalf("finish_segment na empresa B falhou: err=%v msg=%s", err, finBResp.Message)
	}

	// 5. Passageira na Empresa A avalia Motorista B (da Empresa B) com sucesso!
	rateBResp, err := paxClient.Send(protocol.ActionRateDriver, protocol.RateDriverRequest{
		ReservationID: res.ID,
		RideID:        rideB.ID,
		SegmentID:     rideB.Segments[0].ID,
		DriverID:      drvB.ID,
		Score:         5,
		Comment:       "Excelente viagem no trecho da Empresa B!",
	})
	if err != nil || rateBResp.Success != "success" {
		t.Fatalf("rate_driver inter-empresa falhou: err=%v msg=%s", err, rateBResp.Message)
	}

	// 6. Motorista B (Empresa B) avalia Passageira Alice (Empresa A)
	ratePaxResp, err := drvBClient.Send(protocol.ActionRatePassenger, protocol.RatePassengerRequest{
		RideID:      rideB.ID,
		PassengerID: paxUser.ID,
		Score:       4,
		Comment:     "Passageira de outra empresa muito educada.",
	})
	if err != nil || ratePaxResp.Success != "success" {
		t.Fatalf("rate_passenger inter-empresa falhou: err=%v msg=%s", err, ratePaxResp.Message)
	}

	// 7. Passageira A registra denúncia contra Motorista A (da Empresa A) e contra Motorista B
	compBResp, err := paxClient.Send(protocol.ActionFileComplaint, protocol.FileComplaintRequest{
		ReservationID: res.ID,
		RideID:        rideB.ID,
		TargetID:      drvB.ID,
		Comment:       "Ar condicionado desligado durante o trajeto.",
	})
	if err != nil || compBResp.Success != "success" {
		t.Fatalf("file_complaint inter-empresa falhou: err=%v msg=%s", err, compBResp.Message)
	}
	_ = drvA

	// 8. Auditoria cruzada a partir da Empresa C (nó terceiro!): consulta reputação do Motorista B e busca itinerários
	paxCClient, err := RegisterAndLoginPassenger(
		cluster.Nodes["empresa-c"].Server.TCPAddr(),
		"Carla Auditora",
		"carla-inter@vaijunto.test",
		"Senha@123",
	)
	if err != nil {
		t.Fatalf("registrar usuária na empresa C: %v", err)
	}
	defer paxCClient.Close()

	repFromCResp, err := paxCClient.Send(protocol.ActionGetUserReputation, protocol.GetUserReputationRequest{
		UserID: drvB.ID,
	})
	if err != nil || repFromCResp.Success != "success" {
		t.Fatalf("consulta de reputação pela empresa C falhou: err=%v msg=%s", err, repFromCResp.Message)
	}
	var drvBRep models.UserReputation
	_ = json.Unmarshal(repFromCResp.Payload, &drvBRep)
	if drvBRep.TotalRatings != 1 || drvBRep.AverageScore != 5.0 || drvBRep.ComplaintCount != 1 {
		t.Fatalf("reputação agregada inconsistente vista da empresa C: %+v", drvBRep)
	}

	// Cria segunda carona futura do Motorista B para testar DriverRating no search_itineraries + cancel_ride propagado
	tomorrow := now.Add(24 * time.Hour)
	createA2Resp, _ := drvAClient.Send(protocol.ActionCreateRide, protocol.CreateRideRequest{
		DepartureAt: tomorrow,
		Segments: []protocol.CreateStageRequest{
			{
				Origin: enum.Salvador, Destination: enum.FeiraDeSantana,
				DepartureAt: tomorrow, ArrivalAt: tomorrow.Add(1 * time.Hour),
				PriceCents: 3500, AvailableSeats: 2,
			},
		},
	})
	var rideA2 models.Ride
	_ = json.Unmarshal(createA2Resp.Payload, &rideA2)

	createB2Resp, _ := drvBClient.Send(protocol.ActionCreateRide, protocol.CreateRideRequest{
		DepartureAt: tomorrow.Add(2 * time.Hour),
		Segments: []protocol.CreateStageRequest{
			{
				Origin: enum.FeiraDeSantana, Destination: enum.Lencois,
				DepartureAt: tomorrow.Add(2 * time.Hour), ArrivalAt: tomorrow.Add(5 * time.Hour),
				PriceCents: 6000, AvailableSeats: 2,
			},
		},
	})
	var rideB2 models.Ride
	_ = json.Unmarshal(createB2Resp.Payload, &rideB2)

	searchResp, err := paxCClient.Send(protocol.ActionSearchItineraries, protocol.SearchItinerariesRequest{
		Origin:      enum.Salvador,
		Destination: enum.Lencois,
		Date:        tomorrow,
	})
	if err != nil || searchResp.Success != "success" {
		t.Fatalf("busca na empresa C falhou: err=%v msg=%s", err, searchResp.Message)
	}
	var itineraries []protocol.ItineraryResponse
	_ = json.Unmarshal(searchResp.Payload, &itineraries)
	if len(itineraries) == 0 {
		t.Fatalf("esperava ao menos 1 itinerário retornado para amanhã")
	}
	foundRatedDriverB := false
	for _, it := range itineraries {
		for _, seg := range it.Segments {
			if seg.RideID == rideB2.ID && seg.DriverRating == 5.0 && seg.DriverRatingCount == 1 {
				foundRatedDriverB = true
			}
		}
	}
	if !foundRatedDriverB {
		t.Fatalf("itinerário retornado em search_itineraries deveria conter DriverRating=5.0 do Motorista B: %+v", itineraries)
	}

	// 9. Passageira Alice (Empresa A) reserva rideA2 + rideB2; depois Motorista B (Empresa B) cancela rideB2!
	book2Resp, err := paxClient.Send(protocol.ActionConfirmReservation, protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{RideID: rideA2.ID, SegmentID: rideA2.Segments[0].ID, CompanyID: "empresa-a"},
			{RideID: rideB2.ID, SegmentID: rideB2.Segments[0].ID, CompanyID: "empresa-b"},
		},
	})
	if err != nil || book2Resp.Success != "success" {
		t.Fatalf("segunda reserva multi-empresa falhou: err=%v msg=%s", err, book2Resp.Message)
	}
	var res2 protocol.ReservationResponse
	_ = json.Unmarshal(book2Resp.Payload, &res2)

	// Antes do cancelamento, rideA2 deve ter 1 assento disponível
	storedA2Before, _ := cluster.Nodes["empresa-a"].Server.Repository().GetRideByID(rideA2.ID)
	if storedA2Before.Segments[0].AvailableSeats != 1 {
		t.Fatalf("esperava 1 assento disponível em rideA2 antes do cancelamento, obteve %d", storedA2Before.Segments[0].AvailableSeats)
	}

	// Motorista B cancela rideB2 -> reserva na Empresa A deve ser cancelada e assento de rideA2 restaurado para 2!
	cancelB2Resp, err := drvBClient.Send(protocol.ActionCancelRide, protocol.CancelRideRequest{
		RideID: rideB2.ID,
	})
	if err != nil || cancelB2Resp.Success != "success" {
		t.Fatalf("cancel_ride na empresa B falhou: err=%v msg=%s", err, cancelB2Resp.Message)
	}

	storedA2After, _ := cluster.Nodes["empresa-a"].Server.Repository().GetRideByID(rideA2.ID)
	if storedA2After.Segments[0].AvailableSeats != 2 {
		t.Fatalf("assento em rideA2 deveria ter sido restaurado para 2 após cancelamento remoto da carona B, obteve %d",
			storedA2After.Segments[0].AvailableSeats)
	}

	storedRes2, _ := cluster.Nodes["empresa-a"].Server.Repository().GetReservationByID(res2.ID)
	if storedRes2.Status != enum.Cancelada {
		t.Fatalf("reserva na empresa A deveria ter sido marcada como Cancelada, status atual: %d", storedRes2.Status)
	}

	// E a reputação do Motorista B deve registrar 1 cancelamento de carona com passageiros
	repAfterCancelResp, _ := paxCClient.Send(protocol.ActionGetUserReputation, protocol.GetUserReputationRequest{
		UserID: drvB.ID,
	})
	var drvBRepAfter models.UserReputation
	_ = json.Unmarshal(repAfterCancelResp.Payload, &drvBRepAfter)
	if drvBRepAfter.DriverCancelledCount != 1 {
		t.Fatalf("esperado driver_cancelled_count=1 após cancelamento com passageiro remoto, obtido %d",
			drvBRepAfter.DriverCancelledCount)
	}

	// 10. Cancelamento pela empresa de origem (Empresa A): Alice (Empresa A) reserva rideA3 (Empresa A) + rideB3 (Empresa B),
	// e o Motorista A (Empresa A) cancela rideA3 -> deve cancelar res3 na Empresa A e liberar o assento e booking de rideB3 na Empresa B!
	dayAfter := tomorrow.Add(24 * time.Hour)
	createA3Resp, _ := drvAClient.Send(protocol.ActionCreateRide, protocol.CreateRideRequest{
		DepartureAt: dayAfter,
		Segments: []protocol.CreateStageRequest{
			{
				Origin: enum.Salvador, Destination: enum.FeiraDeSantana,
				DepartureAt: dayAfter, ArrivalAt: dayAfter.Add(1 * time.Hour),
				PriceCents: 3500, AvailableSeats: 2,
			},
		},
	})
	var rideA3 models.Ride
	_ = json.Unmarshal(createA3Resp.Payload, &rideA3)

	createB3Resp, _ := drvBClient.Send(protocol.ActionCreateRide, protocol.CreateRideRequest{
		DepartureAt: dayAfter.Add(2 * time.Hour),
		Segments: []protocol.CreateStageRequest{
			{
				Origin: enum.FeiraDeSantana, Destination: enum.Lencois,
				DepartureAt: dayAfter.Add(2 * time.Hour), ArrivalAt: dayAfter.Add(5 * time.Hour),
				PriceCents: 6000, AvailableSeats: 2,
			},
		},
	})
	var rideB3 models.Ride
	_ = json.Unmarshal(createB3Resp.Payload, &rideB3)

	book3Resp, err := paxClient.Send(protocol.ActionConfirmReservation, protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{RideID: rideA3.ID, SegmentID: rideA3.Segments[0].ID, CompanyID: "empresa-a"},
			{RideID: rideB3.ID, SegmentID: rideB3.Segments[0].ID, CompanyID: "empresa-b"},
		},
	})
	if err != nil || book3Resp.Success != "success" {
		t.Fatalf("terceira reserva multi-empresa falhou: err=%v msg=%s", err, book3Resp.Message)
	}
	var res3 protocol.ReservationResponse
	_ = json.Unmarshal(book3Resp.Payload, &res3)

	storedB3Before, _ := cluster.Nodes["empresa-b"].Server.Repository().GetRideByID(rideB3.ID)
	if storedB3Before.Segments[0].AvailableSeats != 1 {
		t.Fatalf("esperava 1 assento disponível em rideB3 antes do cancelamento, obteve %d", storedB3Before.Segments[0].AvailableSeats)
	}

	cancelA3Resp, err := drvAClient.Send(protocol.ActionCancelRide, protocol.CancelRideRequest{
		RideID: rideA3.ID,
	})
	if err != nil || cancelA3Resp.Success != "success" {
		t.Fatalf("cancel_ride de rideA3 na empresa A falhou: err=%v msg=%s", err, cancelA3Resp.Message)
	}

	storedRes3, _ := cluster.Nodes["empresa-a"].Server.Repository().GetReservationByID(res3.ID)
	if storedRes3.Status != enum.Cancelada {
		t.Fatalf("reserva res3 na empresa A deveria estar Cancelada após cancel_ride local, status atual: %d", storedRes3.Status)
	}

	storedB3After, _ := cluster.Nodes["empresa-b"].Server.Repository().GetRideByID(rideB3.ID)
	if storedB3After.Segments[0].AvailableSeats != 2 {
		t.Fatalf("assento em rideB3 na empresa B deveria voltar para 2 após cancelamento da carona A, obteve %d",
			storedB3After.Segments[0].AvailableSeats)
	}

	paxListB3Resp, err := drvBClient.Send(protocol.ActionGetRidePassengers, protocol.GetRidePassengersRequest{
		RideID: rideB3.ID,
	})
	if err != nil || paxListB3Resp.Success != "success" {
		t.Fatalf("get_ride_passengers de rideB3 falhou: err=%v msg=%s", err, paxListB3Resp.Message)
	}
	var paxListB3 protocol.RidePassengersResponse
	_ = json.Unmarshal(paxListB3Resp.Payload, &paxListB3)
	if len(paxListB3.Segments) != 1 || len(paxListB3.Segments[0].Passengers) != 0 {
		t.Fatalf("Alice não deveria mais constar nos passageiros de rideB3 após cancelamento de rideA3: %+v", paxListB3)
	}
}
