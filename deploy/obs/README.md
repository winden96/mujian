# Huawei OBS production configuration

These files are reviewable templates. Applying an ECS agency, a public bucket
policy, CORS, a custom domain, or a certificate changes Huawei Cloud state and
must only happen after the action-time confirmation required by the rollout
plan.

## Required settings

- Bucket: `mujianai` in `cn-north-4`.
- Bucket ACL: private.
- Public bucket policy: apply `public-read-policy.json`. It exposes only
  `mujian/prod/public/*`; do not replace the resource with `mujianai/*`.
- ECS agency policy: apply `ecs-agency-policy.json` to a dedicated agency and
  attach that agency to the production ECS. No permanent AK/SK is required.
- CORS rule:
  - Allowed origin: `https://www.mujianai.com`
  - Allowed methods: `GET`, `HEAD`
  - Allowed headers: `*`
  - Exposed headers: `ETag`, `Content-Type`, `Content-Length`, `Cache-Control`,
    `Content-Disposition`
  - Cache duration: `3600`
- Custom domain: bind `static.mujianai.com` to the bucket and host the matching
  CCM certificate. DNS must use the CNAME value shown by the OBS console.

## Verification

After the application has uploaded a test object:

```sh
curl --fail --silent --show-error --head \
  https://static.mujianai.com/mujian/prod/public/generations/TEST_ID/result.png
curl --fail --silent --show-error --head \
  -H 'Origin: https://www.mujianai.com' \
  https://static.mujianai.com/mujian/prod/public/generations/TEST_ID/result.png
```

Both requests must return the expected MIME type. The second must also return
`Access-Control-Allow-Origin: https://www.mujianai.com`. A request outside the
public prefix must return `403`, matching the production acceptance check.
