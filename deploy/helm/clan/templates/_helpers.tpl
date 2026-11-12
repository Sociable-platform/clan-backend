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

{{/*
A Go service (platform, social, realtime): Service + Deployment. Arguments (a dict):
  root       the chart's root context ($)
  name       platform | social | realtime (also the image name and the Service name)
  port       the port the service listens on
  replicas   number of pods
  recreate   true: stop the old pod before starting the new one (realtime)
  resources  requests and limits
  env        list of env entries for the service; the init container reuses them
  dbURLVar   the env name holding the database URL (the migrations use it too)
  keySecret  the Secret with the JWT key this service may see, keyFile its file name
*/}}
{{- define "clan.goService" -}}
{{- $root := .root }}
{{- $sel := dict "root" $root "name" .name }}
{{- $img := $root.Values.image }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .name }}
  labels:
    {{- include "clan.labels" $root | nindent 4 }}
spec:
  selector:
    {{- include "clan.selector" $sel | nindent 4 }}
  ports:
    - name: http
      port: {{ .port }}
      targetPort: http
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .name }}
  labels:
    {{- include "clan.labels" $root | nindent 4 }}
    {{- include "clan.selector" $sel | nindent 4 }}
spec:
  replicas: {{ .replicas }}
  {{- if .recreate }}
  strategy:
    type: Recreate
  {{- end }}
  selector:
    matchLabels:
      {{- include "clan.selector" $sel | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "clan.labels" $root | nindent 8 }}
        {{- include "clan.selector" $sel | nindent 8 }}
      annotations:
        # A changed password or key restarts the pods, so they read the new value.
        checksum/secrets: {{ include (print $root.Template.BasePath "/secrets.yaml") $root | sha256sum }}
    spec:
      {{- if and $root.Values.secrets.registryUsername $root.Values.secrets.registryPassword }}
      imagePullSecrets:
        - name: gitlab-registry
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
      initContainers:
        # Applies the pending migrations, then exits; the service starts only after it succeeded.
        # Running it in every pod is safe: golang-migrate takes a lock in Postgres, and already
        # applied migrations are skipped. Until Postgres is ready it fails and is retried.
        - name: migrate
          image: {{ printf "%s/%s-migrate:%s" $img.repository .name $img.tag }}
          imagePullPolicy: {{ $img.pullPolicy }}
          args:
            - -path=/migrations
            - -database=$({{ .dbURLVar }})
            - up
          env:
            {{- toYaml .env | nindent 12 }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
      containers:
        - name: {{ .name }}
          image: {{ printf "%s/%s:%s" $img.repository .name $img.tag }}
          imagePullPolicy: {{ $img.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .port }}
          env:
            {{- toYaml .env | nindent 12 }}
          volumeMounts:
            - name: jwt
              mountPath: /run/secrets/{{ .keyFile }}
              subPath: {{ .keyFile }}
              readOnly: true
          # /health answers while the process runs; /ready only when the database is reachable.
          startupProbe:
            httpGet:
              path: /health
              port: http
            periodSeconds: 2
            failureThreshold: 30
          livenessProbe:
            httpGet:
              path: /health
              port: http
            periodSeconds: 10
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /ready
              port: http
            periodSeconds: 5
            failureThreshold: 3
          resources:
            {{- toYaml .resources | nindent 12 }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
      volumes:
        - name: jwt
          secret:
            secretName: {{ .keySecret }}
            defaultMode: 0444
{{- end }}
