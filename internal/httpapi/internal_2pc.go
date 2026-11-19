package httpapi

import (
	"log"
	"net/http"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/consortium"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// PrepareRequest carrega os parâmetros da Fase 1 do 2PC.
type PrepareRequest struct {
	TxID                 string    `json:"tx_id" binding:"required"`
	CoordinatorCompanyID string    `json:"coordinator_company_id" binding:"required"`
	LamportClock         uint64    `json:"lamport_clock"`
	ReservationID        uuid.UUID `json:"reservation_id,omitempty"`
	PassengerID          uuid.UUID `json:"passenger_id" binding:"required"`
	PassengerName        string    `json:"passenger_name,omitempty"`
	PassengerEmail       string    `json:"passenger_email,omitempty"`
	RideID               uuid.UUID `json:"ride_id" binding:"required"`
}

// PrepareResponse responde ao coordenador sobre a aceitação do lock.
type PrepareResponse struct {
	OK           bool   `json:"ok"`
	LamportClock uint64 `json:"lamport_clock"`
	Reason       string `json:"reason,omitempty"`
}

// CommitRequest carrega os parâmetros da Fase 2 de confirmação do 2PC.
type CommitRequest struct {
	TxID                 string    `json:"tx_id" binding:"required"`
	CoordinatorCompanyID string    `json:"coordinator_company_id" binding:"required"`
	RideID               uuid.UUID `json:"ride_id"`
	ReservationID        uuid.UUID `json:"reservation_id,omitempty"`
	PassengerID          uuid.UUID `json:"passenger_id,omitempty"`
	PassengerName        string    `json:"passenger_name,omitempty"`
	PassengerEmail       string    `json:"passenger_email,omitempty"`
}

// CommitResponse responde ao coordenador sobre a efetivação da reserva.
type CommitResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// AbortRequest carrega os parâmetros da Fase 2 de cancelamento do 2PC.
type AbortRequest struct {
	TxID                 string `json:"tx_id" binding:"required"`
	CoordinatorCompanyID string `json:"coordinator_company_id" binding:"required"`
}

// AbortResponse responde ao coordenador sobre o descarte do lock.
type AbortResponse struct {
	OK bool `json:"ok"`
}

// StatusResponse retorna o estado atual de um lock/transação.
type StatusResponse struct {
	TxID      string `json:"tx_id"`
	SegmentID string `json:"segment_id"`
	Status    string `json:"status"` // "PREPARED", "COMMITTED", "NOT_FOUND"
}

func (s *HTTPServer) setTxMeta(segmentID, txID string, meta txPrepareMeta) {
	s.txMetaMu.Lock()
	defer s.txMetaMu.Unlock()
	s.txMeta[segmentID+":"+txID] = meta
}

func (s *HTTPServer) getTxMeta(segmentID, txID string) (txPrepareMeta, bool) {
	s.txMetaMu.RLock()
	defer s.txMetaMu.RUnlock()
	meta, exists := s.txMeta[segmentID+":"+txID]
	return meta, exists
}

func (s *HTTPServer) deleteTxMeta(segmentID, txID string) {
	s.txMetaMu.Lock()
	defer s.txMetaMu.Unlock()
	delete(s.txMeta, segmentID+":"+txID)
}

func (s *HTTPServer) handlePrepare(c *gin.Context) {
	segmentIDParam := c.Param("segmentID")
	segmentID, err := uuid.Parse(segmentIDParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "segmentID inválido"})
		return
	}

	var req PrepareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "corpo da requisição inválido: " + err.Error()})
		return
	}

	log.Printf(
		"[2pc] tx_id=%s role=participant company=%s stage=PREPARE_RECEIVED coordinator=%s segment_id=%s",
		req.TxID, s.companyID, req.CoordinatorCompanyID, segmentIDParam,
	)

	// 1. Atualiza o relógio lógico de Lamport com o timestamp recebido
	currentClock := s.clock.Update(req.LamportClock)

	// 2. Consulta a carona e verifica a disponibilidade física do assento
	ride, err := s.repo.GetRideByID(req.RideID)
	if err != nil || !ride.CanBook() {
		c.JSON(http.StatusConflict, PrepareResponse{
			OK:           false,
			LamportClock: currentClock,
			Reason:       "ride_unavailable",
		})
		return
	}

	availableSeats := 0
	foundSegment := false
	for _, stage := range ride.Segments {
		if stage.ID == segmentID {
			if stage.GetStatus() == models.StageStatusCompleted {
				c.JSON(http.StatusConflict, PrepareResponse{
					OK:           false,
					LamportClock: currentClock,
					Reason:       "segment_already_completed",
				})
				return
			}
			availableSeats = stage.AvailableSeats
			foundSegment = true
			break
		}
	}

	if !foundSegment || availableSeats <= 0 {
		c.JSON(http.StatusConflict, PrepareResponse{
			OK:           false,
			LamportClock: currentClock,
			Reason:       "seat_unavailable",
		})
		return
	}

	// 3. Tenta adquirir o lock no LockStore com resolução de conflito de precedência
	lock := consortium.SegmentLock{
		TxID:          req.TxID,
		CoordinatorID: req.CoordinatorCompanyID,
		LamportClock:  req.LamportClock,
	}

	acquired, evictedTxID, err := s.lockStore.TryAcquire(segmentIDParam, lock)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Se desbancou uma transação anterior perdedora (não preparada), anota o abort no WAL
	if evictedTxID != "" {
		log.Printf("[httpapi] transação %s desalojada por prioridade lógica superior de %s", evictedTxID, req.TxID)
		_ = s.wal.Append(consortium.WALEntry{
			TxID:          evictedTxID,
			Role:          "participant",
			Status:        consortium.TxAborted,
			CoordinatorID: req.CoordinatorCompanyID,
			SegmentIDs:    []string{segmentIDParam},
			UpdatedAt:     time.Now(),
		})
		s.deleteTxMeta(segmentIDParam, evictedTxID)
	}

	if !acquired {
		c.JSON(http.StatusConflict, PrepareResponse{
			OK:           false,
			LamportClock: currentClock,
			Reason:       "lock_conflict",
		})
		return
	}

	// 4. Marca o lock como PREPARADO na memória para impedir desalojamentos e expiração por TTL
	s.lockStore.MarkPrepared(segmentIDParam, req.TxID)

	// 5. Salva os metadados temporários para o commit
	s.setTxMeta(segmentIDParam, req.TxID, txPrepareMeta{
		RideID:               req.RideID,
		ReservationID:        req.ReservationID,
		PassengerID:          req.PassengerID,
		PassengerName:        req.PassengerName,
		PassengerEmail:       req.PassengerEmail,
		CoordinatorCompanyID: req.CoordinatorCompanyID,
	})

	// 6. Registra o PREPARE de forma durável no Write-Ahead Log
	// Se a escrita no disco falhar, o voto afirmativo NÃO PODE ser emitido!
	if err := s.wal.Append(consortium.WALEntry{
		TxID:           req.TxID,
		Role:           "participant",
		Status:         consortium.TxPrepared,
		CoordinatorID:  req.CoordinatorCompanyID,
		SegmentIDs:     []string{segmentIDParam},
		ReservationID:  req.ReservationID,
		PassengerID:    req.PassengerID,
		PassengerName:  req.PassengerName,
		PassengerEmail: req.PassengerEmail,
		UpdatedAt:      time.Now(),
	}); err != nil {
		log.Printf("[httpapi] falha crítica ao registrar PREPARED no WAL tx_id=%s: %v", req.TxID, err)
		s.lockStore.Release(segmentIDParam, req.TxID)
		s.deleteTxMeta(segmentIDParam, req.TxID)
		c.JSON(http.StatusInternalServerError, PrepareResponse{
			OK:           false,
			LamportClock: currentClock,
			Reason:       "wal_write_failed",
		})
		return
	}

	log.Printf(
		"[2pc] tx_id=%s role=participant company=%s stage=PREPARED segment_id=%s result=SUCCESS",
		req.TxID, s.companyID, segmentIDParam,
	)

	c.JSON(http.StatusOK, PrepareResponse{
		OK:           true,
		LamportClock: currentClock,
	})
}

func (s *HTTPServer) handleCommit(c *gin.Context) {
	segmentIDParam := c.Param("segmentID")
	segmentID, err := uuid.Parse(segmentIDParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "segmentID inválido"})
		return
	}

	var req CommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "corpo da requisição inválido: " + err.Error()})
		return
	}

	log.Printf(
		"[2pc] tx_id=%s role=participant company=%s stage=COMMIT_RECEIVED coordinator=%s segment_id=%s",
		req.TxID, s.companyID, req.CoordinatorCompanyID, segmentIDParam,
	)

	s.clock.Tick()

	// 1. Consulta o estado da transação no WAL segundo a máquina de estados
	txState, walEntries, err := s.wal.GetTxState(req.TxID)
	if err != nil {
		log.Printf("[httpapi] erro ao consultar estado no WAL tx_id=%s: %v", req.TxID, err)
		c.JSON(http.StatusInternalServerError, CommitResponse{OK: false, Reason: "wal_read_error"})
		return
	}

	if txState == consortium.TxAborted {
		c.JSON(http.StatusConflict, CommitResponse{
			OK:     false,
			Reason: "transaction_aborted",
		})
		return
	}

	meta, hasMeta := s.getTxMeta(segmentIDParam, req.TxID)

	// 2. Determina a carona
	rideID := req.RideID
	if rideID == uuid.Nil && hasMeta {
		rideID = meta.RideID
	}
	if rideID == uuid.Nil {
		// Tenta encontrar a carona pelo segmento se não veio na requisição
		if r, err := s.repo.GetRideBySegmentID(segmentID); err == nil {
			rideID = r.ID
		}
	}

	// 3. Se o WAL já contém COMMITTED (decisão tomada anteriormente):
	if txState == consortium.TxCommitted {
		// CONDIÇÃO ESSENCIAL: nunca responder sucesso sem verificar e garantir que o efeito foi aplicado
		if !s.repo.IsTxApplied(req.TxID, segmentID) {
			if rideID == uuid.Nil {
				c.JSON(http.StatusBadRequest, CommitResponse{
					OK:     false,
					Reason: "ride_id_not_found",
				})
				return
			}
			if err := s.repo.DecrementSeatIdempotent(rideID, segmentID, req.TxID); err != nil {
				log.Printf("[httpapi] erro ao aplicar decremento pendente tx_id=%s: %v", req.TxID, err)
				c.JSON(http.StatusInternalServerError, CommitResponse{
					OK:     false,
					Reason: err.Error(),
				})
				return
			}
		}

		if err := s.persistBookingFromCommit(req, meta, walEntries, segmentID, rideID); err != nil {
			log.Printf("[httpapi] erro ao salvar booking idempotente tx_id=%s: %v", req.TxID, err)
		}

		s.lockStore.Release(segmentIDParam, req.TxID)
		s.deleteTxMeta(segmentIDParam, req.TxID)
		c.JSON(http.StatusOK, CommitResponse{OK: true})
		return
	}

	// 4. Se não era COMMITTED, deve estar PREPARED (no WAL ou lock ativo)
	lock, hasLock := s.lockStore.Get(segmentIDParam)
	isPrepared := (txState == consortium.TxPrepared) || (hasLock && lock.TxID == req.TxID)

	if !isPrepared {
		c.JSON(http.StatusGone, CommitResponse{
			OK:     false,
			Reason: "lock_not_found",
		})
		return
	}

	if rideID == uuid.Nil {
		c.JSON(http.StatusBadRequest, CommitResponse{
			OK:     false,
			Reason: "ride_id_not_found",
		})
		return
	}

	// 5. REGISTRO DURÁVEL DA DECISÃO NO WAL PRIMEIRO
	if err := s.wal.Append(consortium.WALEntry{
		TxID:          req.TxID,
		Role:          "participant",
		Status:        consortium.TxCommitted,
		CoordinatorID: req.CoordinatorCompanyID,
		SegmentIDs:    []string{segmentIDParam},
		UpdatedAt:     time.Now(),
	}); err != nil {
		log.Printf("[httpapi] erro ao gravar COMMITTED no WAL tx_id=%s: %v", req.TxID, err)
		c.JSON(http.StatusInternalServerError, CommitResponse{
			OK:     false,
			Reason: "wal_write_failed",
		})
		return
	}

	// 6. APLICAÇÃO EFETIVA DO EFEITO NO REPOSITÓRIO (IDEMPOTENTE)
	if err := s.repo.DecrementSeatIdempotent(rideID, segmentID, req.TxID); err != nil {
		log.Printf("[httpapi] erro ao decrementar assento no commit tx_id=%s: %v", req.TxID, err)
		c.JSON(http.StatusInternalServerError, CommitResponse{
			OK:     false,
			Reason: err.Error(),
		})
		return
	}

	if err := s.persistBookingFromCommit(req, meta, walEntries, segmentID, rideID); err != nil {
		log.Printf("[httpapi] erro ao salvar booking remoto tx_id=%s: %v", req.TxID, err)
		c.JSON(http.StatusInternalServerError, CommitResponse{
			OK:     false,
			Reason: err.Error(),
		})
		return
	}

	// 7. Libera o lock de pré-reserva e limpa mapeamentos em memória
	s.lockStore.Release(segmentIDParam, req.TxID)
	s.deleteTxMeta(segmentIDParam, req.TxID)

	log.Printf(
		"[2pc] tx_id=%s role=participant company=%s stage=COMMITTED segment_id=%s result=SUCCESS",
		req.TxID, s.companyID, segmentIDParam,
	)

	c.JSON(http.StatusOK, CommitResponse{OK: true})
}

func (s *HTTPServer) persistBookingFromCommit(
	req CommitRequest,
	meta txPrepareMeta,
	walEntries []consortium.WALEntry,
	segmentID, rideID uuid.UUID,
) error {
	resID := req.ReservationID
	paxID := req.PassengerID
	paxName := req.PassengerName
	paxEmail := req.PassengerEmail
	companyID := req.CoordinatorCompanyID

	if resID == uuid.Nil {
		resID = meta.ReservationID
	}
	if paxID == uuid.Nil {
		paxID = meta.PassengerID
	}
	if paxName == "" {
		paxName = meta.PassengerName
	}
	if paxEmail == "" {
		paxEmail = meta.PassengerEmail
	}
	if companyID == "" {
		companyID = meta.CoordinatorCompanyID
	}

	for _, entry := range walEntries {
		if resID == uuid.Nil && entry.ReservationID != uuid.Nil {
			resID = entry.ReservationID
		}
		if paxID == uuid.Nil && entry.PassengerID != uuid.Nil {
			paxID = entry.PassengerID
		}
		if paxName == "" && entry.PassengerName != "" {
			paxName = entry.PassengerName
		}
		if paxEmail == "" && entry.PassengerEmail != "" {
			paxEmail = entry.PassengerEmail
		}
	}

	if paxID == uuid.Nil {
		return nil
	}

	return s.repo.SaveSegmentBooking(&models.SegmentBooking{
		TxID:           req.TxID,
		ReservationID:  resID,
		RideID:         rideID,
		SegmentID:      segmentID,
		PassengerID:    paxID,
		PassengerName:  paxName,
		PassengerEmail: paxEmail,
		CompanyID:      companyID,
		CreatedAt:      time.Now(),
	})
}

func (s *HTTPServer) handleAbort(c *gin.Context) {
	segmentIDParam := c.Param("segmentID")

	var req AbortRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "corpo da requisição inválido: " + err.Error()})
		return
	}

	log.Printf(
		"[2pc] tx_id=%s role=participant company=%s stage=ABORT_RECEIVED coordinator=%s segment_id=%s",
		req.TxID, s.companyID, req.CoordinatorCompanyID, segmentIDParam,
	)

	s.clock.Tick()

	// 1. Consulta o estado da transação no WAL
	txState, _, err := s.wal.GetTxState(req.TxID)
	if err != nil {
		log.Printf("[httpapi] erro ao consultar WAL no abort tx_id=%s: %v", req.TxID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "wal_read_error"})
		return
	}

	// 2. Se a transação já foi confirmada com COMMITTED, a decisão de commit PREVALECE!
	// Mensagem de abort tardia não reverte assento nem altera o estado consolidado.
	if txState == consortium.TxCommitted {
		log.Printf("[httpapi] abort tardio ignorado para transação já confirmada com COMMIT tx_id=%s", req.TxID)
		c.JSON(http.StatusOK, AbortResponse{OK: true})
		return
	}

	// 3. Se já estava ABORTED, resposta idempotente
	if txState == consortium.TxAborted {
		c.JSON(http.StatusOK, AbortResponse{OK: true})
		return
	}

	// 4. Libera lock se ainda estiver ativo para esse txID
	s.lockStore.Release(segmentIDParam, req.TxID)
	s.deleteTxMeta(segmentIDParam, req.TxID)

	// 5. Registra o abort no WAL tratando possíveis falhas de escrita
	if err := s.wal.Append(consortium.WALEntry{
		TxID:          req.TxID,
		Role:          "participant",
		Status:        consortium.TxAborted,
		CoordinatorID: req.CoordinatorCompanyID,
		SegmentIDs:    []string{segmentIDParam},
		UpdatedAt:     time.Now(),
	}); err != nil {
		log.Printf("[httpapi] falha ao registrar ABORTED no WAL tx_id=%s: %v", req.TxID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "wal_write_failed"})
		return
	}

	log.Printf(
		"[2pc] tx_id=%s role=participant company=%s stage=ABORTED segment_id=%s result=SUCCESS",
		req.TxID, s.companyID, segmentIDParam,
	)

	c.JSON(http.StatusOK, AbortResponse{OK: true})
}

func (s *HTTPServer) handleStatus(c *gin.Context) {
	segmentIDParam := c.Param("segmentID")
	txID := c.Param("txID")

	if lock, exists := s.lockStore.Get(segmentIDParam); exists && lock.TxID == txID {
		c.JSON(http.StatusOK, StatusResponse{
			TxID:      txID,
			SegmentID: segmentIDParam,
			Status:    string(consortium.TxPrepared),
		})
		return
	}

	txState, _, err := s.wal.GetTxState(txID)
	if err == nil && txState != "" {
		c.JSON(http.StatusOK, StatusResponse{
			TxID:      txID,
			SegmentID: segmentIDParam,
			Status:    string(txState),
		})
		return
	}

	c.JSON(http.StatusOK, StatusResponse{
		TxID:      txID,
		SegmentID: segmentIDParam,
		Status:    "NOT_FOUND",
	})
}

// ReleaseRequest carrega os parâmetros para liberação de assento decorrente de cancelamento.
type ReleaseRequest struct {
	RideID               uuid.UUID `json:"ride_id"`
	ReservationID        uuid.UUID `json:"reservation_id,omitempty"`
	PassengerID          uuid.UUID `json:"passenger_id,omitempty"`
	CoordinatorCompanyID string    `json:"coordinator_company_id"`
}

// ReleaseResponse responde sobre o sucesso da liberação do assento.
type ReleaseResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func (s *HTTPServer) handleRelease(c *gin.Context) {
	segmentIDParam := c.Param("segmentID")
	segmentID, err := uuid.Parse(segmentIDParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, ReleaseResponse{OK: false, Reason: "segment_id_invalido"})
		return
	}

	var req ReleaseRequest
	_ = c.ShouldBindJSON(&req)

	rideID := req.RideID
	if rideID == uuid.Nil {
		ride, err := s.repo.GetRideBySegmentID(segmentID)
		if err != nil {
			c.JSON(http.StatusNotFound, ReleaseResponse{OK: false, Reason: "carona_nao_encontrada"})
			return
		}
		rideID = ride.ID
	}

	if err := s.repo.IncrementSeat(rideID, segmentID); err != nil {
		log.Printf("[httpapi] falha ao incrementar assento no release trecho=%s: %v", segmentID, err)
		c.JSON(http.StatusConflict, ReleaseResponse{OK: false, Reason: err.Error()})
		return
	}

	_ = s.repo.CancelSegmentBooking(segmentID, req.ReservationID, req.PassengerID)

	s.clock.Tick()
	log.Printf("[httpapi] assento do trecho %s liberado via release", segmentID)
	c.JSON(http.StatusOK, ReleaseResponse{OK: true})
}
