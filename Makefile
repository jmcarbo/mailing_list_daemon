build_docker:
	docker build -t listuka .

run_docker:
	docker run -ti --name listuka -h mail.joanmarc-carbo.info -p 25:2525 -p 8080:8080 listuka /bin/bash
