# Retrying upload finalization

`POST /markUpdateAsUploaded/{branch}` accepts retries for the same update ID.
After a successful commit, the durable `.check` marker causes retries to return
200 without comparing the update to itself or deleting its files. This also
applies to historical updates after a newer release has been published.
Concurrent requests for the same upload are serialized within a server process.
Distinct uploads with identical contents retain the existing 406 behavior.

Verification checks each unique file once, using at most eight storage requests
at a time. Missing files and storage failures both prevent finalization. A
verification failure retains the existing cleanup of the uncommitted upload.
Writing the `.check` marker must succeed before the API returns success.

This addresses the September 7 incident in DealSeek run 34137402370. Railway
logged successful original finalizations after about 20 seconds, followed by
406 responses and deletion of the same update IDs on repeated requests.

Regression tests cover concurrent, sequential, and historical retries on both
platforms, unchanged-content rejection, missing files, storage errors, duplicate
asset paths, and the concurrency bound. The sequential retry returns 406 with
the original handler and 200 with this correction.
