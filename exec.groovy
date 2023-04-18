pipeline {
    agent any
    stages {
        stage("build"){
            steps {
                // rupasa k naap p chal ja please
                sh 'docker build -t public-ecr-aws:latest .'
            }
        }
        stage("exec") {
            steps{
                withCredentials([usernamePassword(credentialsId: 'redis-connection-string', passwordVariable: 'REDIS_PASSWORD', usernameVariable: 'REDIS_HOST')]) {
                    sh '''docker run --rm \
                    -e REDIS_HOST=${REDIS_HOST} \
                    -e REDIS_PASSWORD=${REDIS_PASSWORD} \
                    -v $PWD:/pwd \
                    public-ecr-aws:latest ${ARGS}
                    '''
                }
                // withCredentials([string(credentialsId: 'gotify-url', variable: 'GOTIFY_URL')]) {
                //     sh '''
                //         FINDINGS=$(wc -l output.log)
                //         curl "${GOTIFY_URL}" -F "title=public ecr scan finished" -F "message=total public repositories found: ${FINDINGS}" -F "priority=6"
                //     '''
                // }
            }
        }
        stage("cleanup"){
            steps {
                sh 'docker image rm public-ecr-aws:latest'
                echo 'cleaning up'
                sh 'rm -rf * && rm -rf .git*'
                sh 'ls -alh'
            }
        }
    }
}