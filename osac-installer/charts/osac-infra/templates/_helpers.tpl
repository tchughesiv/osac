{{- define "osac-infra.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "osac-infra.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "osac-infra.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "osac-infra.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "osac-infra.hookLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "osac-infra.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "osac-infra.keycloakNamespace" -}}
{{- if eq .Values.keycloak.mode "external" -}}
{{- required "keycloak.external.namespace is required when keycloak.mode=external" .Values.keycloak.external.namespace -}}
{{- else -}}
keycloak
{{- end -}}
{{- end }}

{{- define "osac-infra.keycloakClientSecretName" -}}
{{- if eq .Values.keycloak.mode "external" -}}
{{- required "keycloak.external.clientSecretName is required when keycloak.mode=external" .Values.keycloak.external.clientSecretName -}}
{{- else -}}
keycloak-client-secrets
{{- end -}}
{{- end }}
