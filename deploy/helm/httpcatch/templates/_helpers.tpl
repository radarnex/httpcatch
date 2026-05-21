{{/*
Expand the name of the chart.
*/}}
{{- define "httpcatch.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name.
*/}}
{{- define "httpcatch.fullname" -}}
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

{{- define "httpcatch.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every resource.
*/}}
{{- define "httpcatch.labels" -}}
helm.sh/chart: {{ include "httpcatch.chart" . }}
{{ include "httpcatch.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "httpcatch.selectorLabels" -}}
app.kubernetes.io/name: {{ include "httpcatch.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
componentLabels emits the component label for a sub-resource. Pass the
component name as the second argument:
  {{ include "httpcatch.componentLabels" (list . "admin") }}
*/}}
{{- define "httpcatch.componentLabels" -}}
{{- $ctx := index . 0 -}}
{{- $component := index . 1 -}}
{{ include "httpcatch.labels" $ctx }}
app.kubernetes.io/component: {{ $component }}
{{- end -}}

{{/*
annotations merges per-resource annotations with .Values.commonAnnotations
and emits the `annotations:` block only when the merged map is non-empty.
Pass a dict:
  {{ include "httpcatch.annotations" (dict "ctx" . "extra" .Values.service.admin.annotations) }}
The output is unindented; the caller is responsible for nindent.
*/}}
{{- define "httpcatch.annotations" -}}
{{- $ctx := .ctx -}}
{{- $merged := merge (deepCopy (default (dict) .extra)) (default (dict) $ctx.Values.commonAnnotations) -}}
{{- if $merged -}}
annotations:
{{ toYaml $merged | indent 2 -}}
{{- end -}}
{{- end -}}

{{- define "httpcatch.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "httpcatch.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "httpcatch.image" -}}
{{- $registry := .Values.image.registry | default "docker.io" -}}
{{- $repo := .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s/%s@%s" $registry $repo .Values.image.digest -}}
{{- else -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repo $tag -}}
{{- end -}}
{{- end -}}

{{- define "httpcatch.configMapName" -}}
{{- printf "%s-config" (include "httpcatch.fullname" .) -}}
{{- end -}}

{{- define "httpcatch.adminTokenSecretName" -}}
{{- if .Values.adminToken.existingSecret -}}
{{- .Values.adminToken.existingSecret -}}
{{- else -}}
{{- printf "%s-admin-token" (include "httpcatch.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "httpcatch.adminTokenSecretKey" -}}
{{- if .Values.adminToken.existingSecret -}}
{{- default "admin-token" .Values.adminToken.existingSecretKey -}}
{{- else -}}
admin-token
{{- end -}}
{{- end -}}

{{/*
Resolve the admin token to persist in the generated Secret. Order of preference:
  1. Explicit .Values.adminToken.value supplied by the operator.
  2. Existing value already stored in the Secret (sticky across upgrades).
  3. Freshly generated 32-byte random string (first install).
*/}}
{{- define "httpcatch.adminToken" -}}
{{- if .Values.adminToken.value -}}
{{- .Values.adminToken.value -}}
{{- else -}}
{{- $name := include "httpcatch.adminTokenSecretName" . -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace $name -}}
{{- if and $existing $existing.data (index $existing.data "admin-token") -}}
{{- index $existing.data "admin-token" | toString | b64dec -}}
{{- else -}}
{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Render the runtime config.yaml from values. Admin token is intentionally
omitted here — it is injected via env var from the Secret so it never lands
in the ConfigMap.
*/}}
{{- define "httpcatch.config" -}}
capture_port: {{ .Values.capturePort | int }}
queue_size: {{ .Values.queueSize | int }}
body_cap: {{ .Values.bodyCap | int }}
max_events_payload: {{ .Values.maxEventsPayload | int }}
workers: {{ .Values.workers | int }}
service_header: {{ .Values.serviceHeader | quote }}
log_format: {{ .Values.logFormat | quote }}
sinks:
  stdout: {{ .Values.sinks.stdout }}
  memory: {{ .Values.sinks.memory }}
  memory_capacity: {{ .Values.sinks.memoryCapacity | int }}
  sqlite: {{ .Values.persistence.enabled }}
{{- if .Values.persistence.enabled }}
  sqlite_path: {{ printf "%s/httpcatch.db" .Values.persistence.mountPath | quote }}
{{- end }}
admin:
  bind: "0.0.0.0:8081"
  session_ttl: {{ .Values.admin.sessionTTL | quote }}
  session_secure: {{ .Values.admin.sessionSecure }}
{{- $r := .Values.redaction }}
{{- if or $r.headers $r.queryParams $r.jsonPaths $r.regex $r.cookies }}
redaction:
{{- with $r.headers }}
  headers:
{{ toYaml . | indent 4 }}
{{- end }}
{{- with $r.queryParams }}
  query_params:
{{ toYaml . | indent 4 }}
{{- end }}
{{- with $r.jsonPaths }}
  json_paths:
{{ toYaml . | indent 4 }}
{{- end }}
{{- with $r.regex }}
  regex:
{{ toYaml . | indent 4 }}
{{- end }}
{{- with $r.cookies }}
  cookies:
{{ toYaml . | indent 4 }}
{{- end }}
{{- end }}
{{- with .Values.extraConfig }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
podTemplateMetadata emits the pod-template metadata block (labels + checksum
annotations + user-supplied podAnnotations) shared by Deployment and
StatefulSet. Indent at the caller via nindent 6.
*/}}
{{- define "httpcatch.podTemplateMetadata" -}}
labels:
{{ include "httpcatch.labels" . | indent 2 }}
{{- with .Values.podLabels }}
{{ toYaml . | indent 2 }}
{{- end }}
annotations:
  checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
  checksum/secret: {{ include (print $.Template.BasePath "/secret.yaml") . | sha256sum }}
{{- with .Values.podAnnotations }}
{{ toYaml . | indent 2 }}
{{- end }}
{{- end -}}

{{/*
Pod template shared by Deployment and StatefulSet.
*/}}
{{- define "httpcatch.podSpec" -}}
serviceAccountName: {{ include "httpcatch.serviceAccountName" . }}
automountServiceAccountToken: {{ .Values.serviceAccount.automountServiceAccountToken }}
terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
{{- with .Values.image.pullSecrets }}
imagePullSecrets:
{{ toYaml . }}
{{- end }}
{{- with .Values.podSecurityContext }}
securityContext:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .Values.priorityClassName }}
priorityClassName: {{ . }}
{{- end }}
{{- with .Values.nodeSelector }}
nodeSelector:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
{{ toYaml . }}
{{- end }}
{{- with .Values.affinity }}
affinity:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .Values.topologySpreadConstraints }}
topologySpreadConstraints:
{{ toYaml . }}
{{- end }}
{{- with .Values.extraInitContainers }}
initContainers:
{{ toYaml . }}
{{- end }}
containers:
  - name: httpcatch
    image: {{ include "httpcatch.image" . }}
    imagePullPolicy: {{ .Values.image.pullPolicy }}
    args:
      - serve
      - --config
      - /etc/httpcatch/config.yaml
    env:
      - name: HTTPCATCH_ADMIN_TOKEN
        valueFrom:
          secretKeyRef:
            name: {{ include "httpcatch.adminTokenSecretName" . }}
            key: {{ include "httpcatch.adminTokenSecretKey" . }}
{{- with .Values.extraEnv }}
{{ toYaml . | indent 6 }}
{{- end }}
{{- with .Values.extraEnvFrom }}
    envFrom:
{{ toYaml . | indent 6 }}
{{- end }}
    ports:
      - name: capture
        containerPort: {{ .Values.capturePort }}
        protocol: TCP
      - name: admin
        containerPort: 8081
        protocol: TCP
{{- if .Values.probes.liveness.enabled }}
    livenessProbe:
      httpGet:
        path: /healthz
        port: admin
      initialDelaySeconds: {{ .Values.probes.liveness.initialDelaySeconds }}
      periodSeconds: {{ .Values.probes.liveness.periodSeconds }}
      timeoutSeconds: {{ .Values.probes.liveness.timeoutSeconds }}
      successThreshold: {{ .Values.probes.liveness.successThreshold }}
      failureThreshold: {{ .Values.probes.liveness.failureThreshold }}
{{- end }}
{{- if .Values.probes.readiness.enabled }}
    readinessProbe:
      httpGet:
        path: /healthz
        port: admin
      initialDelaySeconds: {{ .Values.probes.readiness.initialDelaySeconds }}
      periodSeconds: {{ .Values.probes.readiness.periodSeconds }}
      timeoutSeconds: {{ .Values.probes.readiness.timeoutSeconds }}
      successThreshold: {{ .Values.probes.readiness.successThreshold }}
      failureThreshold: {{ .Values.probes.readiness.failureThreshold }}
{{- end }}
{{- if and .Values.persistence.enabled .Values.probes.startup.enabled }}
    startupProbe:
      httpGet:
        path: /healthz
        port: admin
      initialDelaySeconds: {{ .Values.probes.startup.initialDelaySeconds }}
      periodSeconds: {{ .Values.probes.startup.periodSeconds }}
      timeoutSeconds: {{ .Values.probes.startup.timeoutSeconds }}
      successThreshold: {{ .Values.probes.startup.successThreshold }}
      failureThreshold: {{ .Values.probes.startup.failureThreshold }}
{{- end }}
{{- with .Values.resources }}
    resources:
{{ toYaml . | indent 6 }}
{{- end }}
{{- with .Values.containerSecurityContext }}
    securityContext:
{{ toYaml . | indent 6 }}
{{- end }}
    volumeMounts:
      - name: config
        mountPath: /etc/httpcatch
        readOnly: true
{{- if .Values.persistence.enabled }}
      - name: data
        mountPath: {{ .Values.persistence.mountPath }}
{{- end }}
{{- with .Values.extraVolumeMounts }}
{{ toYaml . | indent 6 }}
{{- end }}
{{- with .Values.extraContainers }}
{{ toYaml . | indent 2 }}
{{- end }}
volumes:
  - name: config
    configMap:
      name: {{ include "httpcatch.configMapName" . }}
{{- if and .Values.persistence.enabled .Values.persistence.existingClaim }}
  - name: data
    persistentVolumeClaim:
      claimName: {{ .Values.persistence.existingClaim }}
{{- end }}
{{- with .Values.extraVolumes }}
{{ toYaml . | indent 2 }}
{{- end }}
{{- end -}}
