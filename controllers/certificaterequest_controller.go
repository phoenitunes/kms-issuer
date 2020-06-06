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
	"fmt"

	"encoding/pem"

	kmsiapi "github.com/Skyscanner/kms-issuer/api/v1alpha1"
	kmsca "github.com/Skyscanner/kms-issuer/pkg/kmsca"
	"github.com/go-logr/logr"
	apiutil "github.com/jetstack/cert-manager/pkg/api/util"
	cmapi "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1alpha2"
	cmmeta "github.com/jetstack/cert-manager/pkg/apis/meta/v1"
	pkiutil "github.com/jetstack/cert-manager/pkg/util/pki"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CertificateRequestReconciler reconciles a StepIssuer object.
type CertificateRequestReconciler struct {
	client.Client
	Log      logr.Logger
	Recorder record.EventRecorder
	KMSCA    *kmsca.KMSCA
}

// +kubebuilder:rbac:groups=cert-manager.io,resources=certificaterequests,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificaterequests/status,verbs=get;update;patch

// Reconcile will read and validate a KMSIssuer resource associated to the
// CertificateRequest resource, and it will sign the CertificateRequest with the
// provisioner in the KMSIssuer.
func (r *CertificateRequestReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("certificaterequests", req.NamespacedName)

	// Fetch the CertificateRequest resource being reconciled.
	// Just ignore the request if the certificate request has been deleted.
	cr := new(cmapi.CertificateRequest)
	if err := r.Client.Get(ctx, req.NamespacedName, cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to retrieve CertificateRequest resource")
		return ctrl.Result{}, err
	}

	// Check the CertificateRequest's issuerRef and if it does not match the api
	// group name, log a message at a debug level and stop processing.
	if cr.Spec.IssuerRef.Group != "" && cr.Spec.IssuerRef.Group != kmsiapi.GroupVersion.Group {
		log.V(4).Info("resource does not specify an issuerRef group name that we are responsible for", "group", cr.Spec.IssuerRef.Group)
		return ctrl.Result{}, nil
	}

	// If the certificate data is already set then we skip this request as it
	// has already been completed in the past.
	if len(cr.Status.Certificate) > 0 {
		log.V(4).Info("existing certificate data found in status, skipping already completed CertificateRequest")
		return ctrl.Result{}, nil
	}

	// TODO: Do we allow signing intermidate CAs?
	// if cr.Spec.IsCA {
	// 	log.Info("step certificate does not support online signing of CA certificates")
	// 	return ctrl.Result{}, nil
	// }

	// Fetch the KMSIssuer resource
	issuer := kmsiapi.KMSIssuer{}
	issNamespaceName := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      cr.Spec.IssuerRef.Name,
	}
	if err := r.Client.Get(ctx, issNamespaceName, &issuer); err != nil {
		log.Error(err, "failed to retrieve KMSIssuer resource", "namespace", req.Namespace, "name", cr.Spec.IssuerRef.Name)
		_ = r.setStatus(ctx, cr, cmmeta.ConditionFalse, cmapi.CertificateRequestReasonPending, "Failed to retrieve KMSIssuer resource %s: %v", issNamespaceName, err)
		return ctrl.Result{}, err
	}

	// Check if the KMSIssuer resource has been marked Ready
	if !issuer.Status.IsReady() {
		err := fmt.Errorf("resource %s is not ready", issNamespaceName)
		log.Error(err, "failed to retrieve StepIssuer resource", "namespace", req.Namespace, "name", cr.Spec.IssuerRef.Name)
		_ = r.setStatus(ctx, cr, cmmeta.ConditionFalse, cmapi.CertificateRequestReasonPending, "StepIssuer resource %s is not Ready", issNamespaceName)
		return ctrl.Result{}, err
	}

	// Sign CertificateRequest
	cert, err := pkiutil.GenerateTemplateFromCertificateRequest(cr)
	if err != nil {
		log.Error(err, "failed to decode certificate request")
		return ctrl.Result{}, r.setStatus(ctx, cr, cmmeta.ConditionFalse, cmapi.CertificateRequestReasonFailed, "Failed to decode certificate request: %v", err)
	}
	parent, err := pkiutil.DecodeX509CertificateBytes(issuer.Status.Certificate)
	if err != nil {
		log.Error(err, "failed to decode issuer public certificate")
		return ctrl.Result{}, r.setStatus(ctx, cr, cmmeta.ConditionFalse, cmapi.CertificateRequestReasonFailed, "Failed to decode issuer public certificate: %v", err)
	}

	signed, err := r.KMSCA.SignCertificate(&kmsca.IssueCertificateInput{
		KeyId:     issuer.Spec.KeyId,
		Parent:    parent,
		Cert:      cert,
		PublicKey: cert.PublicKey,
	})
	if err != nil {
		log.Error(err, "failed to sign certificate request")
		return ctrl.Result{}, r.setStatus(ctx, cr, cmmeta.ConditionFalse, cmapi.CertificateRequestReasonFailed, "Failed to sign certificate request: %v", err)
	}
	cr.Status.Certificate = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: signed.Raw})
	cr.Status.CA = issuer.Status.Certificate
	log.Info(string(cr.Status.Certificate))
	return ctrl.Result{}, r.setStatus(ctx, cr, cmmeta.ConditionTrue, cmapi.CertificateRequestReasonIssued, "Certificate issued")
}

// SetupWithManager initializes the CertificateRequest controller into the
// controller runtime.
func (r *CertificateRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cmapi.CertificateRequest{}).
		Complete(r)
}

// stepIssuerHasCondition will return true if the given StepIssuer resource has
// a condition matching the provided StepIssuerCondition. Only the Type and
// Status field will be used in the comparison, meaning that this function will
// return 'true' even if the Reason, Message and LastTransitionTime fields do
// not match.
// func stepIssuerHasCondition(iss api.StepIssuer, c api.StepIssuerCondition) bool {
// 	existingConditions := iss.Status.Conditions
// 	for _, cond := range existingConditions {
// 		if c.Type == cond.Type && c.Status == cond.Status {
// 			return true
// 		}
// 	}
// 	return false
// }

func (r *CertificateRequestReconciler) setStatus(ctx context.Context, cr *cmapi.CertificateRequest, status cmmeta.ConditionStatus, reason, message string, args ...interface{}) error {
	completeMessage := fmt.Sprintf(message, args...)
	apiutil.SetCertificateRequestCondition(cr, cmapi.CertificateRequestConditionReady, status, reason, completeMessage)

	// Fire an Event to additionally inform users of the change
	eventType := core.EventTypeNormal
	if status == cmmeta.ConditionFalse {
		eventType = core.EventTypeWarning
	}
	r.Recorder.Event(cr, eventType, reason, completeMessage)

	return r.Status().Update(ctx, cr)
}