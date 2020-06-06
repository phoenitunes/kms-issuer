/*
Copyright 2020 Skyscanner Limited.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"
	"fmt"

	"crypto/x509/pkix"
	"encoding/pem"
	"time"

	kmsiapi "github.com/Skyscanner/kms-issuer/api/v1alpha1"
	"github.com/Skyscanner/kms-issuer/pkg/kmsca"
	"github.com/aws/aws-sdk-go/service/kms/kmsiface"
	"github.com/go-logr/logr"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var kmsApi kmsiface.KMSAPI

// KMSIssuerReconciler reconciles a KMSIssuer object.
type KMSIssuerReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	KMSCA    *kmsca.KMSCA
}

// +kubebuilder:rbac:groups=cert-manager.skyscanner.net,resources=kmsissuers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.skyscanner.net,resources=kmsissuers/status,verbs=get;update;patch

// NewKMSIssuerReconciler Initialise a new KMSIssuerReconciler
func NewKMSIssuerReconciler(mgr manager.Manager, ca *kmsca.KMSCA) *KMSIssuerReconciler {
	return &KMSIssuerReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("kmsissuer_controller"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("kmsissuer_controller"),
		KMSCA:    ca,
	}
}

// Reconcile KMSIssuer resources.
func (r *KMSIssuerReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("kms-issuer", req.NamespacedName)

	// retrieve the KMSIssuer resource to reconcile.
	issuer := &kmsiapi.KMSIssuer{}
	if err := r.Client.Get(ctx, req.NamespacedName, issuer); err != nil {
		log.Error(err, "failed to retrieve KMSIssuer resource")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// validation
	if len(issuer.Spec.KeyId) == 0 {
		return ctrl.Result{}, r.manageFailure(ctx, log, issuer, errors.New("INVALID KeyId"), fmt.Sprintf("Not a valid key: %s", issuer.Spec.KeyId))
	}

	// Generate ca certificate
	if len(issuer.Status.Certificate) == 0 {
		log.Info("generate certificate")
		cert, err := r.KMSCA.GenerateCertificateAuthorityCertificate(&kmsca.GenerateCertificateAuthorityCertificateInput{
			KeyId: issuer.Spec.KeyId,
			Subject: pkix.Name{
				CommonName: issuer.Spec.CommonName,
			},
			NotBefore: time.Now(),
			NotAfter:  time.Now().Add(issuer.Spec.Duration.Duration),
		})
		if err != nil {
			return ctrl.Result{}, r.manageFailure(ctx, log, issuer, err, "Failed to generate the Certificate Authority Certificate")
		}
		issuer.Status.Certificate = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		if err := r.Client.Status().Update(ctx, issuer); err != nil {
			return ctrl.Result{}, r.manageFailure(ctx, log, issuer, err, "Failed to update the issuer with the issued Certificate")
		}
	}
	return ctrl.Result{}, r.manageSuccess(ctx, log, issuer)
}

// SetupWithManager is pre-generated
func (r *KMSIssuerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kmsiapi.KMSIssuer{}).
		Complete(r)
}

// manageSuccess
func (r *KMSIssuerReconciler) manageSuccess(ctx context.Context, log logr.Logger, issuer *kmsiapi.KMSIssuer) error {
	reason := kmsiapi.KMSIssuerReasonIssued
	msg := ""
	log.Info("successfuly reconciled issuer")
	r.Recorder.Event(issuer, core.EventTypeNormal, reason, msg)
	issuer.Status.SetCondition(kmsiapi.NewCondition(kmsiapi.ConditionReady, kmsiapi.ConditionTrue, reason, msg))
	if err := r.Client.Status().Update(ctx, issuer); err != nil {
		return err
	}
	return nil
}

// manageFailure
func (r *KMSIssuerReconciler) manageFailure(ctx context.Context, log logr.Logger, issuer *kmsiapi.KMSIssuer, issue error, message string) error {
	reason := kmsiapi.KMSIssuerReasonFailed
	log.Error(issue, message)
	r.Recorder.Event(issuer, core.EventTypeWarning, reason, message)
	issuer.Status.SetCondition(kmsiapi.NewCondition(kmsiapi.ConditionReady, kmsiapi.ConditionFalse, reason, message))
	if err := r.Client.Status().Update(ctx, issuer); err != nil {
		return err
	}
	return nil
}