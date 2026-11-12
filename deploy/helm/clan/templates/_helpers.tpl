{{/* Labels on every object of the chart (the selector labels below add name and instance). */}}
{{- define "clan.labels" -}}
app.kubernetes.io/part-of: clan
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{/* Selector labels of one component: include with (dict "root" $ "name" "postgres"). */}}
{{- define "clan.selector" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{/*
A password from the secrets file: required, at least 12 letters and digits only (it goes into URLs).
Include with (dict "name" "postgresPassword" "value" .Values.secrets.postgresPassword).
*/}}
{{- define "clan.password" -}}
{{- $v := required (printf "secrets.%s is required (see secrets.example.yaml)" .name) .value -}}
{{- if not (regexMatch "^[A-Za-z0-9]{12,}$" $v) -}}
{{- fail (printf "secrets.%s must be at least 12 letters and digits only: it goes into database and broker URLs" .name) -}}
{{- end -}}
{{- $v -}}
{{- end }}

{{/* An optional token: empty, or at least 32 characters. Same arguments as clan.password. */}}
{{- define "clan.token" -}}
{{- if and .value (lt (len .value) 32) -}}
{{- fail (printf "secrets.%s must be empty or at least 32 characters" .name) -}}
{{- end -}}
{{- .value -}}
{{- end }}
