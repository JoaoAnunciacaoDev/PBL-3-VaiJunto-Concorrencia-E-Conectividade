package httpapi

import (
	"net/http"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type RemoteRideCancelledRequest struct {
	CancelledCompanyID string    `json:"cancelled_company_id"`
	RideID             uuid.UUID `json:"ride_id,omitempty"`
}

func (s *HTTPServer) handleReceiveReputationRecord(c *gin.Context) {
	var rating models.Rating
	if err := c.ShouldBindJSON(&rating); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "registro de reputação inválido: " + err.Error()})
		return
	}

	if rating.AuthorID == uuid.Nil || rating.TargetID == uuid.Nil || rating.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "author_id, target_id e type são obrigatórios"})
		return
	}

	if err := s.repo.SaveRating(&rating); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	s.clock.Tick()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *HTTPServer) handleGetReputation(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userID inválido"})
		return
	}

	rep, err := s.repo.GetReputationByUserID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, rep)
}

func (s *HTTPServer) handleRemoteRideCancelled(c *gin.Context) {
	reservationID, err := uuid.Parse(c.Param("reservationID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reservationID inválido"})
		return
	}

	var req RemoteRideCancelledRequest
	_ = c.ShouldBindJSON(&req)

	s.mu.Lock()
	handler := s.rideCancelledHandler
	s.mu.Unlock()

	if handler != nil {
		if err := handler(reservationID, req.CancelledCompanyID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else {
		if _, err := s.repo.CancelReservationByRideCancellation(reservationID, s.companyID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	s.clock.Tick()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
