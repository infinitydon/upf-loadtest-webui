{{- define "upf-loadtest-webui.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "upf-loadtest-webui.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "upf-loadtest-webui.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "upf-loadtest-webui.labels" -}}
app.kubernetes.io/name: {{ include "upf-loadtest-webui.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}
