{{- define "dnscale-webhook.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dnscale-webhook.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "dnscale-webhook.selfSignedIssuer" -}}
{{ printf "%s-selfsign" (include "dnscale-webhook.fullname" .) }}
{{- end -}}

{{- define "dnscale-webhook.rootCAIssuer" -}}
{{ printf "%s-ca" (include "dnscale-webhook.fullname" .) }}
{{- end -}}

{{- define "dnscale-webhook.rootCACertificate" -}}
{{ printf "%s-ca" (include "dnscale-webhook.fullname" .) }}
{{- end -}}

{{- define "dnscale-webhook.servingCertificate" -}}
{{ printf "%s-webhook-tls" (include "dnscale-webhook.fullname" .) }}
{{- end -}}
